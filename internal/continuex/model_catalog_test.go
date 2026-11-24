package continuex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexCachedCatalogFiltersHiddenModelsAndPrivateMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models_cache.json")
	raw := `{"identity":"PRIVATE-IDENTITY","models":[{"slug":"visible-model","display_name":"Readable name","visibility":"list"},{"slug":"hidden-model","visibility":"hide"},{"slug":"disabled-model","hidden":true},{"slug":"visible-model","visibility":"list"}]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := readCodexModelCache(path)
	if err != nil || len(catalog.Models) != 1 || catalog.Items[0].ID != "visible-model" || catalog.Items[0].Name != "Readable name" || catalog.Warning == "" {
		t.Fatalf("bad cache catalog: %+v %v", catalog, err)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "PRIVATE-IDENTITY") {
		t.Fatal("private cache metadata leaked")
	}
}

func TestCursorModelRowsPreserveIDsNamesAndPolicyLabels(t *testing.T) {
	rows := "Available models\n\x1b[32mauto - Auto (current, default)\x1b[0m\nmodel-a - Friendly A\nmodel-b - Friendly B (NO ZDR)\nmodel-c  Friendly C\n"
	items := parseCursorModels(rows)
	if len(items) != 4 || items[0].ID != "auto" || items[0].Name != "Auto" || !items[0].Default || items[2].Name != "Friendly B (NO ZDR)" {
		t.Fatalf("bad model rows: %+v", items)
	}
}

func TestModelCatalogNormalizationRejectsLogsAndKeepsOrder(t *testing.T) {
	catalog := HarnessModelCatalog{Items: []HarnessModel{{ID: "model-a"}, {ID: "model-a"}, {ID: "--flag"}, {ID: `{"id":"not-a-model"}`}, {ID: "two words"}, {ID: "provider/model[1m]"}}}
	catalog.normalize()
	if strings.Join(catalog.Models, ",") != "model-a,provider/model[1m]" {
		t.Fatalf("bad IDs: %v", catalog.Models)
	}
}

func TestConfiguredModelCatalogDoesNotExposeProviderKeys(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kimi"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "default_model = 'work'\n[models.work]\nmodel = 'provider-model'\n[models.backup]\nmodel = 'other-model'\n[providers.private]\napi_key = 'SECRET-CREDENTIAL'\n"
	if err := os.WriteFile(filepath.Join(home, ".kimi", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := configuredHarnessModelsAt("kimi", home)
	catalog.normalize()
	if err != nil || len(catalog.Models) != 2 || catalog.Models[0] != "work" || catalog.Models[1] != "backup" {
		t.Fatalf("configured models: %+v %v", catalog, err)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "SECRET-CREDENTIAL") {
		t.Fatal("provider key leaked")
	}
}

func TestModelCatalogProbeChild(t *testing.T) {
	mode := os.Getenv("NX_MODEL_CATALOG_CHILD")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	for scanner.Scan() {
		var row struct {
			ID        int    `json:"id"`
			Method    string `json:"method"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Params    struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if mode == "claude" {
			if row.Type != "control_request" {
				t.Fatal("Model listing sent a user message")
			}
			fmt.Fprintln(os.Stdout, `{"type":"control_response","response":{"subtype":"success","request_id":"nexus_models","response":{"models":[{"value":"sonnet","displayName":"Sonnet"},{"value":"custom/model","displayName":"Custom"},{"value":"default","displayName":"Default"}]}}}`)
			continue
		}
		switch row.Method {
		case "initialize":
			initialized = true
			fmt.Fprintf(os.Stdout, `{"id":%d,"result":{}}`+"\n", row.ID)
		case "initialized":
			if !initialized {
				t.Fatal("notification before initialize")
			}
		case "model/list":
			if !initialized {
				t.Fatal("model/list before initialize")
			}
			if row.Params.Cursor == "" {
				fmt.Fprintf(os.Stdout, `{"id":%d,"result":{"data":[{"id":"a","model":"model-a","displayName":"Friendly A","isDefault":true},{"id":"hidden","hidden":true}],"nextCursor":"page2"}}`+"\n", row.ID)
			} else {
				fmt.Fprintf(os.Stdout, `{"id":%d,"result":{"data":[{"id":"b","model":"model-b","displayName":"Friendly B"}],"nextCursor":null}}`+"\n", row.ID)
			}
		default:
			t.Fatalf("Model listing attempted non-discovery method %s", row.Method)
		}
	}
}

func TestCodexModelDiscoveryPaginatesWithoutStartingAThread(t *testing.T) {
	t.Setenv("NX_MODEL_CATALOG_CHILD", "codex")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := HarnessAvailability{Agent: "codex", Name: "Codex", Executable: os.Args[0], Prefix: []string{"-test.run=^TestModelCatalogProbeChild$", "--"}}
	catalog, err := codexModels(ctx, status)
	catalog.normalize()
	if err != nil || strings.Join(catalog.Models, ",") != "model-a,model-b" || catalog.Items[0].Name != "Friendly A" || !catalog.Items[0].Default {
		t.Fatalf("RPC catalog: %+v %v", catalog, err)
	}
}

func TestClaudeModelDiscoveryUsesOnlyControlInitialization(t *testing.T) {
	t.Setenv("NX_MODEL_CATALOG_CHILD", "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := HarnessAvailability{Agent: "claude", Name: "Claude", Executable: os.Args[0], Prefix: []string{"-test.run=^TestModelCatalogProbeChild$", "--"}}
	catalog, err := claudeModels(ctx, status)
	catalog.normalize()
	if err != nil || strings.Join(catalog.Models, ",") != "sonnet,custom/model" {
		t.Fatalf("SDK catalog: %+v %v", catalog, err)
	}
}
