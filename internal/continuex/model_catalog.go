package continuex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

type HarnessModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"is_default,omitempty"`
}

type HarnessModelCatalog struct {
	Agent   string         `json:"agent"`
	Models  []string       `json:"models"` // Compatible with existing native clients.
	Items   []HarnessModel `json:"items"`
	Source  string         `json:"source"`
	Warning string         `json:"warning,omitempty"`
	// Fallback marks a built-in list used because live discovery failed.
	Fallback  bool      `json:"fallback,omitempty"`
	Cached    bool      `json:"cached,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

// Each CLI listing call is bounded so an unresponsive harness can never hold
// the picker; callers then fall back to a cached or built-in list.
const modelDiscoveryTimeout = 8 * time.Second

// Claude Code has no model-list subcommand. When its SDK initialization
// cannot be read, offer the documented aliases plus current full model IDs.
// The picker's own "Default model" entry covers the "default" alias.
func claudeStaticModels(reason string) HarnessModelCatalog {
	return HarnessModelCatalog{
		Agent:    "claude",
		Source:   "Claude Code built-in model list (static)",
		Fallback: true,
		Warning:  "Couldn't load models: " + strings.TrimSuffix(reason, ".") + ", using defaults. This is a built-in list of Claude Code aliases, not your account's catalog.",
		Items: []HarnessModel{
			{ID: "sonnet", Name: "Sonnet", Description: "Alias for the latest Sonnet model"},
			{ID: "opus", Name: "Opus", Description: "Alias for the latest Opus model"},
			{ID: "haiku", Name: "Haiku", Description: "Alias for the latest Haiku model"},
			{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
			{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
			{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5"},
			{ID: "claude-fable-5-2", Name: "Claude Fable 5.2"},
		},
	}
}

func validHarnessModelID(id string) bool {
	return id != "" && len(id) <= 160 && !strings.HasPrefix(id, "-") && strings.IndexFunc(id, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/@+[],=()-", r))
	}) < 0
}

func (c *HarnessModelCatalog) normalize() {
	seen := map[string]bool{}
	items := make([]HarnessModel, 0, len(c.Items))
	c.Models = make([]string, 0, len(c.Items))
	for _, item := range c.Items {
		item.ID = strings.TrimSpace(item.ID)
		if !validHarnessModelID(item.ID) || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		item.Name = progressLabel(item.Name, 180)
		if item.Name == "" {
			item.Name = item.ID
		}
		item.Description = progressLabel(item.Description, 500)
		items = append(items, item)
		c.Models = append(c.Models, item.ID)
	}
	c.Items = items
}

// Discovery never starts a user turn, changes authentication, or selects a
// paid/fallback model. A catalog is not proof of entitlement or remaining quota.
func DiscoverHarnessModels(ctx context.Context, agent string, refresh bool) (HarnessModelCatalog, error) {
	agent = normalizeAgent(agent)
	status := DetectHarness(agent)
	catalog := HarnessModelCatalog{Agent: agent}
	if status.Executable == "" {
		if agent == "claude" {
			catalog = claudeStaticModels(status.Problem)
			catalog.normalize()
			return catalog, nil
		}
		return catalog, errors.New(status.Problem)
	}
	ctx, cancel := context.WithTimeout(ctx, modelDiscoveryTimeout)
	defer cancel()
	var err error
	switch agent {
	case "codex":
		if !refresh {
			if cached, e := readCodexModelCache(codexModelCachePath()); e == nil && len(cached.Items) > 0 {
				return cached, nil
			}
		}
		catalog, err = codexModels(ctx, status)
		if err != nil {
			if cached, e := readCodexModelCache(codexModelCachePath()); e == nil && len(cached.Items) > 0 {
				cached.Warning = "Live Codex discovery failed; showing its local cached catalog. Availability and quota are not verified. " + err.Error()
				return cached, nil
			}
		}
	case "opencode":
		var raw string
		raw, err = readModelCommand(ctx, status, []string{"models"})
		catalog.Source = "OpenCode provider catalog"
		catalog.Items = parseOpenCodeModels(raw)
	case "cursor", "agent":
		var raw string
		raw, err = readModelCommand(ctx, status, []string{"--disable-auto-update", "--list-models"})
		catalog.Source = "Cursor account model list"
		catalog.Items = parseCursorModels(raw)
	case "claude":
		catalog, err = claudeModels(ctx, status)
		if err != nil {
			catalog = claudeStaticModels(err.Error())
			err = nil
		}
	case "gemini", "kimi":
		catalog, err = configuredHarnessModels(agent)
	default:
		err = errors.New("This harness does not expose a supported in-app model catalog. Use its default or enter a model ID.")
	}
	catalog.Agent = agent
	catalog.normalize()
	if err != nil {
		return catalog, err
	}
	if len(catalog.Items) == 0 && catalog.Warning == "" {
		return catalog, fmt.Errorf("%s returned no model choices. Check its sign-in/configuration, or enter a model ID manually.", status.Name)
	}
	return catalog, nil
}

func readModelCommand(ctx context.Context, status HarnessAvailability, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, status.Executable, append(append([]string{}, status.Prefix...), args...)...)
	hideConsole(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var out, diagnostic cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s model discovery timed out or was cancelled; no chat was started", status.Name)
		}
		if problem := providerDiagnostic(status.Agent, diagnostic.String()); problem != nil {
			return "", problem
		}
		return "", fmt.Errorf("Could not load %s models. Check that you're signed in to %s and that it's up to date", status.Name, status.Name)
	}
	if out.truncated {
		return "", errors.New("Model catalog exceeded the local discovery limit")
	}
	return activityANSI.ReplaceAllString(out.String(), ""), nil
}

// `opencode models` prints one provider/model ID per line; skip log noise.
func parseOpenCodeModels(raw string) []HarnessModel {
	var items []HarnessModel
	for _, line := range strings.Split(activityANSI.ReplaceAllString(raw, ""), "\n") {
		id := strings.TrimSpace(line)
		if strings.Contains(id, "/") && validHarnessModelID(id) {
			items = append(items, HarnessModel{ID: id, Name: id})
		}
	}
	return items
}

var cursorModelRow = regexp.MustCompile(`^\s*(?:[-*•]\s+)?([A-Za-z0-9][A-Za-z0-9._:/+\[\],=-]*)(\s+-\s+|\s{2,}|\s+\()(.+?)\s*$`)
var cursorModelMarker = regexp.MustCompile(`\s+\((?:current|default)(?:,\s*(?:current|default))*\)$`)

func parseCursorModels(raw string) []HarnessModel {
	var items []HarnessModel
	for _, line := range strings.Split(activityANSI.ReplaceAllString(raw, ""), "\n") {
		line = strings.TrimSpace(line)
		match := cursorModelRow.FindStringSubmatch(line)
		if len(match) > 3 {
			id, name := match[1], match[3]
			if strings.HasSuffix(match[2], "(") {
				name = strings.TrimSuffix(name, ")")
			}
			isDefault := strings.Contains(name, "(current, default)") || strings.HasSuffix(name, "(default)")
			name = cursorModelMarker.ReplaceAllString(name, "")
			if strings.Contains(id, "-") || id == "auto" {
				items = append(items, HarnessModel{ID: id, Name: name, Default: isDefault})
			}
		} else if validHarnessModelID(line) && (strings.Contains(line, "-") || line == "auto") {
			items = append(items, HarnessModel{ID: line, Name: line})
		}
	}
	return items
}

func codexModelCachePath() string {
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".codex")
	}
	return filepath.Join(root, "models_cache.json")
}

func readBoundedModelFile(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(out)
}

func readCodexModelCache(path string) (HarnessModelCatalog, error) {
	catalog := HarnessModelCatalog{Agent: "codex", Source: "Codex local model catalog", Warning: "Cached model catalog; refresh to check the CLI catalog. Entries do not guarantee account access or available quota."}
	var data struct {
		Models []struct {
			Slug        string `json:"slug"`
			Name        string `json:"display_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
			Hidden      bool   `json:"hidden"`
		}
	}
	if err := readBoundedModelFile(path, &data); err != nil {
		return catalog, err
	}
	for _, row := range data.Models {
		if !row.Hidden && (row.Visibility == "" || row.Visibility == "list") {
			catalog.Items = append(catalog.Items, HarnessModel{ID: row.Slug, Name: row.Name, Description: row.Description})
		}
	}
	catalog.normalize()
	return catalog, nil
}

func configuredHarnessModels(agent string) (HarnessModelCatalog, error) {
	home, _ := os.UserHomeDir()
	return configuredHarnessModelsAt(agent, home)
}

func configuredHarnessModelsAt(agent, home string) (HarnessModelCatalog, error) {
	catalog := HarnessModelCatalog{Agent: agent, Source: "Local harness configuration", Warning: "This harness has no supported account-wide model-list adapter here. Configured choices are shown when present; use its default or enter a model ID. Access and quota are not verified."}
	if agent == "gemini" {
		var data struct {
			Model struct {
				Name string `json:"name"`
			} `json:"model"`
		}
		_ = readBoundedModelFile(filepath.Join(home, ".gemini", "settings.json"), &data)
		for _, id := range []string{os.Getenv("GEMINI_MODEL"), data.Model.Name} {
			if id != "" {
				catalog.Items = append(catalog.Items, HarnessModel{ID: id, Name: id})
			}
		}
	} else {
		var data struct {
			DefaultModel string `toml:"default_model"`
			Models       map[string]struct {
				Model string `toml:"model"`
			} `toml:"models"`
		}
		file, err := os.Open(filepath.Join(home, ".kimi", "config.toml"))
		if err == nil {
			defer file.Close()
			_, _ = toml.NewDecoder(io.LimitReader(file, 2<<20)).Decode(&data)
		}
		if data.DefaultModel != "" {
			catalog.Items = append(catalog.Items, HarnessModel{ID: data.DefaultModel, Name: data.DefaultModel, Default: true})
		}
		aliases := make([]string, 0, len(data.Models))
		for alias := range data.Models {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			row := data.Models[alias]
			catalog.Items = append(catalog.Items, HarnessModel{ID: alias, Name: row.Model})
		}
	}
	return catalog, nil
}
