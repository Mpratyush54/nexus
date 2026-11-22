package extract

import (
	"crypto/sha256"
	"encoding/hex"
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
	SessionID string `json:"session_id,omitempty"`
}

// Existing is a known memory used for prompt dedup hints. Key is shown in
// the prompt so the LLM can name it in supersedes_key.
type Existing struct {
	Key     string `json:"key,omitempty"`
	Level   string `json:"level,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Content string `json:"content"`
}

// Proposal is one candidate memory.
type Proposal struct {
	Key           string   `json:"key"`
	Content       string   `json:"content"`
	Level         string   `json:"level"`
	Scope         string   `json:"scope"`
	Confidence    float64  `json:"confidence"`
	Explicit      bool     `json:"explicit,omitempty"`
	Source        string   `json:"source,omitempty"`
	Category      string   `json:"category,omitempty"` // architecture | infrastructure | auth | api | conventions | dependencies
	FilesAffected []string `json:"files_affected,omitempty"`
	ToolsUsed     []string `json:"tools_used,omitempty"`
	SupersedesKey string   `json:"supersedes_key,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`     // active | superseded | deprecated
	WeekBucket    string   `json:"week_bucket,omitempty"` // e.g. 2026-W39
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
	LLMError  string // set when OpenRouter was attempted and failed
}

// Limits cap batch size for free-tier OpenRouter usage.
const (
	MaxTurns       = 40
	MaxBatchChars  = 12000
	ThrottleWindow = 5 * time.Minute

	// Session compress allows a fuller transcript window.
	MaxCompressTurns = 120
	MaxCompressChars = 48000
)

// Config holds OpenRouter settings (env or explicit).
type Config struct {
	APIKey        string
	BaseURL       string // default https://openrouter.ai/api/v1
	Model         string // default instruct-capable free model
	CompressModel string // optional stronger model for session compress
	HTTPReferer   string
	AppTitle      string
}

// ConfigFromEnv reads OPENROUTER_* variables.
func ConfigFromEnv() Config {
	return Config{
		APIKey:        strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		BaseURL:       strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		Model:         strings.TrimSpace(os.Getenv("OPENROUTER_MODEL")),
		CompressModel: strings.TrimSpace(os.Getenv("OPENROUTER_COMPRESS_MODEL")),
		HTTPReferer:   strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_REFERER")),
		AppTitle:      strings.TrimSpace(os.Getenv("OPENROUTER_APP_TITLE")),
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

// AllowLLM reports whether an OpenRouter call is permitted for project now.
func (s *Service) AllowLLM(project string) bool {
	return s.allowLLM(project)
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
	return capTurnsLimit(turns, MaxTurns, MaxBatchChars)
}

// CapCompressTurns truncates for session-compress (larger window).
func CapCompressTurns(turns []Turn) []Turn {
	return capTurnsLimit(turns, MaxCompressTurns, MaxCompressChars)
}

func capTurnsLimit(turns []Turn, maxTurns, maxChars int) []Turn {
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
		if len(out) >= maxTurns {
			break
		}
		if chars+len(c) > maxChars && len(out) > 0 {
			break
		}
		out = append(out, Turn{
			Speaker:   strings.TrimSpace(t.Speaker),
			Content:   c,
			Timestamp: t.Timestamp,
			SessionID: strings.TrimSpace(t.SessionID),
		})
		chars += len(c)
	}
	return out
}

// SessionIDFromTurns returns the first non-empty session_id on the batch.
func SessionIDFromTurns(turns []Turn) string {
	for _, t := range turns {
		if id := strings.TrimSpace(t.SessionID); id != "" {
			return id
		}
	}
	return ""
}

// KeyFromContent derives a key for a memory whose writer (caller or LLM)
// supplied none. The readable slug is the first four words of 3+ letters
// ("deploy/with-migrations-applied"); a short content hash is appended
// ("…-3fa9c1") because same-key writes supersede each other, and two
// unrelated memories that merely start with the same words must not. The
// hash is over normalised content (lower-case words), so re-extracting the
// same sentence yields the same key and still dedupes. Only call this for
// auto-generated keys: a key the caller or LLM chose is kept as given.
func KeyFromContent(text string) string {
	toks := tokenize(text)
	var words []string
	for _, t := range toks {
		if len(t) < 3 || len(words) >= 4 {
			continue
		}
		words = append(words, t)
	}
	var slug string
	switch len(words) {
	case 0:
		slug = "misc/note"
	case 1:
		slug = "misc/" + words[0]
	default:
		slug = words[0] + "/" + strings.Join(words[1:], "-")
	}
	sum := sha256.Sum256([]byte(strings.Join(toks, " ")))
	return slug + "-" + hex.EncodeToString(sum[:])[:6]
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
