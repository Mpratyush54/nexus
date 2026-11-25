package continuex

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Claude Code has no model-list command, and its account catalog needs the
// user's own credentials, so Nexus offers Claude Code's documented aliases
// and the current model IDs (https://code.claude.com/docs/en/model-config).
// A live session's advertised list (RecordAdvertisedModels) replaces this
// when one is available. Keep this list current with each model release.
func claudeModelCatalog() HarnessModelCatalog {
	return HarnessModelCatalog{
		Agent:  "claude",
		Source: "Claude Code models",
		Items: []HarnessModel{
			{ID: "opus", Name: "Opus", Description: "Latest Opus (Opus 5.5) for complex work"},
			{ID: "sonnet", Name: "Sonnet", Description: "Latest Sonnet (Sonnet 5.5) for everyday coding"},
			{ID: "haiku", Name: "Haiku", Description: "Latest Haiku (Haiku 5.5), fastest"},
			{ID: "fable", Name: "Fable", Description: "Fable 5.2 for the hardest, longest-running tasks"},
			{ID: "opusplan", Name: "Opus Plan", Description: "Opus while planning, Sonnet while doing"},
			{ID: "claude-fable-5-2", Name: "Claude Fable 5.2"},
			{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
			{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
			{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5"},
		},
	}
}

// Gemini CLI has no model-list command. Its aliases follow the account's
// access; the concrete IDs are the current documented ones.
func geminiModelCatalog() HarnessModelCatalog {
	return HarnessModelCatalog{
		Agent:  "gemini",
		Source: "Gemini CLI models",
		Items: []HarnessModel{
			{ID: "auto", Name: "Auto", Description: "Gemini picks the model for each request"},
			{ID: "pro", Name: "Pro", Description: "Latest Pro model you have access to"},
			{ID: "flash", Name: "Flash", Description: "Latest Flash model, faster"},
			{ID: "flash-lite", Name: "Flash Lite", Description: "Fastest and lightest"},
			{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro (preview)"},
			{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro (preview)"},
			{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
			{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
			{ID: "gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite"},
			{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
			{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
		},
	}
}

func kimiHome(home string, getenv func(string) string) []string {
	if dir := strings.TrimSpace(getenv("KIMI_CODE_HOME")); dir != "" {
		return []string{dir}
	}
	// Kimi Code, then the legacy kimi-cli location.
	return []string{filepath.Join(home, ".kimi-code"), filepath.Join(home, ".kimi")}
}

type kimiConfig struct {
	DefaultModel string `toml:"default_model"`
	Models       map[string]struct {
		Model       string `toml:"model"`
		DisplayName string `toml:"display_name"`
		Provider    string `toml:"provider"`
	} `toml:"models"`
}

func readKimiConfig(home string, getenv func(string) string) kimiConfig {
	var data kimiConfig
	for _, dir := range kimiHome(home, getenv) {
		file, err := os.Open(filepath.Join(dir, "config.toml"))
		if err != nil {
			continue
		}
		_, _ = toml.NewDecoder(io.LimitReader(file, 2<<20)).Decode(&data)
		file.Close()
		return data
	}
	return data
}

// Kimi lists the model aliases set up in its config.toml (login adds the
// managed ones). Only names are read; provider keys are never touched.
func kimiModelCatalog(home string, getenv func(string) string) HarnessModelCatalog {
	catalog := HarnessModelCatalog{Agent: "kimi", Source: "Kimi Code configuration"}
	data := readKimiConfig(home, getenv)
	aliases := make([]string, 0, len(data.Models))
	for alias := range data.Models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		row := data.Models[alias]
		name := row.DisplayName
		if name == "" {
			name = friendlyModelName(row.Model)
		}
		catalog.Items = append(catalog.Items, HarnessModel{ID: alias, Name: name, Default: alias == data.DefaultModel})
	}
	return catalog
}

// configuredDefaultModel is the model the agent uses when none is picked,
// from the agent's own environment and config files. "" when unset.
func configuredDefaultModel(agent, home string, getenv func(string) string) string {
	clean := func(values ...string) string {
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v != "" && v != "default" && validHarnessModelID(v) {
				return v
			}
		}
		return ""
	}
	switch normalizeAgent(agent) {
	case "claude":
		dir := getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		var settings struct {
			Model string `json:"model"`
		}
		_ = readBoundedModelFile(filepath.Join(dir, "settings.json"), &settings)
		return clean(getenv("ANTHROPIC_MODEL"), settings.Model, getenv("ANTHROPIC_DEFAULT_MODEL"))
	case "codex":
		root := getenv("CODEX_HOME")
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		var config struct {
			Model    string `toml:"model"`
			Profile  string `toml:"profile"`
			Profiles map[string]struct {
				Model string `toml:"model"`
			} `toml:"profiles"`
		}
		if file, err := os.Open(filepath.Join(root, "config.toml")); err == nil {
			_, _ = toml.NewDecoder(io.LimitReader(file, 2<<20)).Decode(&config)
			file.Close()
		}
		return clean(config.Profiles[config.Profile].Model, config.Model)
	case "opencode":
		var config struct {
			Model string `json:"model"`
		}
		paths := []string{getenv("OPENCODE_CONFIG")}
		dir := getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		paths = append(paths, filepath.Join(dir, "opencode", "opencode.jsonc"), filepath.Join(dir, "opencode", "opencode.json"))
		for _, path := range paths {
			if path == "" {
				continue
			}
			if raw, err := readBoundedBytes(path); err == nil && json.Unmarshal(stripJSONC(raw), &config) == nil && config.Model != "" {
				break
			}
		}
		return clean(config.Model)
	case "gemini":
		var settings struct {
			Model struct {
				Name string `json:"name"`
			} `json:"model"`
		}
		_ = readBoundedModelFile(filepath.Join(home, ".gemini", "settings.json"), &settings)
		return clean(getenv("GEMINI_MODEL"), settings.Model.Name)
	case "kimi":
		return clean(readKimiConfig(home, getenv).DefaultModel)
	}
	return ""
}

func readBoundedBytes(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 2<<20))
}

// stripJSONC removes // and /* */ comments and trailing commas outside
// strings, so OpenCode's opencode.jsonc decodes as JSON.
func stripJSONC(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			if i < len(raw) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t' || raw[j] == '\r' || raw[j] == '\n') {
				j++
			}
			if j < len(raw) && (raw[j] == '}' || raw[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// applyDefault marks the agent's configured default model, adding it when
// the list does not have it, and names it for the picker's Default entry.
// With nothing configured, an entry the CLI marked as default is used.
func (c *HarnessModelCatalog) applyDefault(id string) {
	if id != "" {
		found := false
		for i := range c.Items {
			c.Items[i].Default = c.Items[i].ID == id
			found = found || c.Items[i].Default
		}
		if !found {
			c.Items = append([]HarnessModel{{ID: id, Default: true}}, c.Items...)
			c.normalize()
		}
	}
	c.DefaultModel, c.DefaultName = "", ""
	for _, item := range c.Items {
		if item.Default {
			c.DefaultModel, c.DefaultName = item.ID, item.Name
			return
		}
	}
}

var modelWordNames = map[string]string{
	"gpt": "GPT", "claude": "Claude", "gemini": "Gemini", "gemma": "Gemma", "kimi": "Kimi", "glm": "GLM",
	"grok": "Grok", "deepseek": "DeepSeek", "qwen": "Qwen", "llama": "Llama", "mistral": "Mistral",
	"oss": "OSS", "ai": "AI", "vl": "VL", "it": "IT", "api": "API", "xhigh": "XHigh",
}

// friendlyModelName turns a raw model ID into a readable name:
// "anthropic/claude-sonnet-4-5" -> "Claude Sonnet 4.5", "gpt-5.3-codex-high"
// -> "GPT-5.3 Codex High", "opencode/big-pickle" -> "Big Pickle".
func friendlyModelName(id string) string {
	base := id
	if i := strings.LastIndex(base, "/"); i >= 0 && i+1 < len(base) {
		base = base[i+1:]
	}
	suffix := ""
	if i := strings.Index(base, "["); i > 0 {
		base, suffix = base[:i], " "+strings.ToUpper(base[i:])
	}
	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	if len(parts) == 0 {
		return id
	}
	isNumber := func(s string) bool {
		return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' }) < 0
	}
	var words []string
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if isNumber(part) && len(part) <= 2 && i+1 < len(parts) && isNumber(parts[i+1]) && len(parts[i+1]) <= 2 && !strings.Contains(part, ".") {
			// "4-5" is version 4.5; an 8-digit date is left alone.
			part += "." + parts[i+1]
			i++
		}
		lower := strings.ToLower(part)
		switch {
		case modelWordNames[lower] != "":
			part = modelWordNames[lower]
		case isNumber(part):
		default:
			runes := []rune(part)
			runes[0] = unicode.ToUpper(runes[0])
			part = string(runes)
		}
		// GPT and its version read as one word: "GPT-5.3".
		if len(words) > 0 && words[len(words)-1] == "GPT" && isNumber(part) {
			words[len(words)-1] += "-" + part
			continue
		}
		words = append(words, part)
	}
	return strings.Join(words, " ") + suffix
}

var providerNames = map[string]string{
	"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google", "google-vertex": "Google Vertex",
	"opencode": "OpenCode", "openrouter": "OpenRouter", "github-copilot": "GitHub Copilot", "nvidia": "NVIDIA",
	"xai": "xAI", "mistral": "Mistral", "deepseek": "DeepSeek", "groq": "Groq", "moonshotai": "Moonshot",
	"amazon-bedrock": "Amazon Bedrock", "azure": "Azure", "ollama": "Ollama", "lmstudio": "LM Studio",
	"kimi-code": "Kimi", "zai": "Z.ai", "cerebras": "Cerebras", "fireworks-ai": "Fireworks", "togetherai": "Together AI",
}

// modelProvider groups a model in the picker.
func modelProvider(agent, id string) string {
	switch normalizeAgent(agent) {
	case "claude":
		return "Anthropic"
	case "codex":
		return "OpenAI"
	case "gemini":
		return "Google"
	}
	if i := strings.Index(id, "/"); i > 0 {
		key := strings.ToLower(id[:i])
		if name, ok := providerNames[key]; ok {
			return name
		}
		return friendlyModelName(key)
	}
	lower := strings.ToLower(id)
	family := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(lower, p) {
				return true
			}
		}
		return false
	}
	switch {
	case normalizeAgent(agent) == "kimi":
		return "Kimi"
	case family("gpt", "o1", "o3", "o4", "codex", "chatgpt"):
		return "OpenAI"
	case family("claude", "sonnet", "opus", "haiku", "fable"):
		return "Anthropic"
	case family("gemini", "gemma"):
		return "Google"
	case family("grok"):
		return "xAI"
	case family("composer", "auto", "cursor"):
		return "Cursor"
	case family("kimi", "k2", "k3"):
		return "Moonshot"
	case family("deepseek"):
		return "DeepSeek"
	case family("qwen"):
		return "Qwen"
	}
	return "Other"
}
