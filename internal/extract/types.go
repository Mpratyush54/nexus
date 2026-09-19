package extract

import (
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Turn is one harvested conversation line.
type Turn struct {
	Speaker   string `json:"speaker"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Existing is a known memory used for prompt dedup hints.
type Existing struct {
	Level   string `json:"level,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Content string `json:"content"`
}

// Proposal is one candidate memory.
type Proposal struct {
	Key        string  `json:"key"`
	Content    string  `json:"content"`
	Level      string  `json:"level"`
	Scope      string  `json:"scope"`
	Confidence float64 `json:"confidence"`
	Explicit   bool    `json:"explicit,omitempty"`
	Source     string  `json:"source,omitempty"`
}

// Provider names returned in Result.Provider.
const (
	ProviderOpenRouter = "openrouter"
	ProviderHeuristic  = "heuristic"
)

// Result is the outcome of one Extract call.
type Result struct {
	Proposals []Proposal
	Provider  string
}

// Limits cap batch size for free-tier OpenRouter usage.
const (
	MaxTurns       = 40
	MaxBatchChars  = 12000
	ThrottleWindow = 5 * time.Minute
)

// Config holds OpenRouter settings (env or explicit).
type Config struct {
	APIKey      string
	BaseURL     string // default https://openrouter.ai/api/v1
	Model       string // default openrouter/free
	HTTPReferer string
	AppTitle    string
}

// ConfigFromEnv reads OPENROUTER_* variables.
func ConfigFromEnv() Config {
	return Config{
		APIKey:      strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		BaseURL:     strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		Model:       strings.TrimSpace(os.Getenv("OPENROUTER_MODEL")),
		HTTPReferer: strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_REFERER")),
		AppTitle:    strings.TrimSpace(os.Getenv("OPENROUTER_APP_TITLE")),
	}
}

// Service runs LLM or heuristic extraction with per-project throttling.
type Service struct {
	Cfg    Config
	Client *Client // nil → built from Cfg
	Now    func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewService builds a Service from config (typically ConfigFromEnv).
func NewService(cfg Config) *Service {
	return &Service{Cfg: cfg, last: map[string]time.Time{}}
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) allowLLM(project string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]time.Time{}
	}
	cur := s.now()
	if last, ok := s.last[project]; ok && cur.Sub(last) < ThrottleWindow {
		return false
	}
	return true
}

func (s *Service) markLLM(project string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]time.Time{}
	}
	s.last[project] = s.now()
}

// CapTurns truncates a turn batch to MaxTurns / MaxBatchChars.
func CapTurns(turns []Turn) []Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]Turn, 0, len(turns))
	chars := 0
	for _, t := range turns {
		c := strings.TrimSpace(t.Content)
		if c == "" {
			continue
		}
		if len(out) >= MaxTurns {
			break
		}
		if chars+len(c) > MaxBatchChars && len(out) > 0 {
			break
		}
		out = append(out, Turn{
			Speaker:   strings.TrimSpace(t.Speaker),
			Content:   c,
			Timestamp: t.Timestamp,
		})
		chars += len(c)
	}
	return out
}

// KeyFromContent derives a short key from content (mirrors daemon heuristic).
func KeyFromContent(text string) string {
	var words []string
	for _, t := range tokenize(text) {
		if len(t) < 3 || len(words) >= 4 {
			continue
		}
		words = append(words, t)
	}
	if len(words) == 0 {
		return "misc/note"
	}
	if len(words) == 1 {
		return "misc/" + words[0]
	}
	return words[0] + "/" + strings.Join(words[1:], "-")
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
