package agentrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RuleStore keeps "Always allow" rules per project in one JSON file, and
// the default permission mode for new chats.
type RuleStore struct {
	mu   sync.Mutex
	path string
	data ruleFile
}

type ruleFile struct {
	DefaultMode string                 `json:"default_mode,omitempty"`
	Projects    map[string][]SavedRule `json:"projects"`
}

// SavedRule is a rule with when it was added, for Settings.
type SavedRule struct {
	Rule
	Label   string    `json:"label"`
	AddedAt time.Time `json:"added_at"`
}

// OpenRuleStore loads path (a missing or damaged file starts empty).
func OpenRuleStore(path string) *RuleStore {
	s := &RuleStore{path: path, data: ruleFile{Projects: map[string][]SavedRule{}}}
	if path == "" {
		return s
	}
	if b, err := os.ReadFile(path); err == nil {
		var f ruleFile
		if json.Unmarshal(b, &f) == nil {
			if f.Projects == nil {
				f.Projects = map[string][]SavedRule{}
			}
			s.data = f
		}
	}
	return s
}

// ProjectKey picks the key rules are saved under: the project ID, or the
// folder for chats without one.
func ProjectKey(projectID, workspace string) string {
	if p := strings.TrimSpace(projectID); p != "" {
		return p
	}
	if w := strings.TrimSpace(workspace); w != "" {
		return "folder:" + strings.ToLower(filepath.Clean(w))
	}
	return ""
}

func (s *RuleStore) Rules(project string) []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.data.Projects[project]
	out := make([]Rule, len(saved))
	for i, r := range saved {
		out[i] = r.Rule
	}
	return out
}

// Add remembers a rule; adding one that exists is a no-op.
func (s *RuleStore) Add(project string, rule Rule) error {
	if project == "" || rule.Tool == "" || (rule.Tool == KindCommand && strings.TrimSpace(rule.Prefix) == "") {
		return nil
	}
	rule.Prefix = strings.Join(strings.Fields(rule.Prefix), " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data.Projects[project] {
		if r.Rule == rule {
			return nil
		}
	}
	s.data.Projects[project] = append(s.data.Projects[project], SavedRule{Rule: rule, Label: rule.Label(), AddedAt: time.Now().UTC()})
	return s.saveLocked()
}

func (s *RuleStore) Remove(project string, rule Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.data.Projects[project]
	kept := list[:0]
	for _, r := range list {
		if r.Rule != rule {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		delete(s.data.Projects, project)
	} else {
		s.data.Projects[project] = kept
	}
	return s.saveLocked()
}

// All lists every project's rules for Settings, sorted by project.
func (s *RuleStore) All() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.data.Projects))
	for k := range s.data.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		rules := append([]SavedRule(nil), s.data.Projects[k]...)
		for i := range rules {
			rules[i].Label = rules[i].Rule.Label() // current wording, whatever was saved
		}
		out = append(out, map[string]any{"project": k, "rules": rules})
	}
	return out
}

func (s *RuleStore) DefaultMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.DefaultMode == "" {
		return ModeEdits
	}
	return NormalizeMode(s.data.DefaultMode)
}

func (s *RuleStore) SetDefaultMode(mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.DefaultMode = NormalizeMode(mode)
	return s.saveLocked()
}

func (s *RuleStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
