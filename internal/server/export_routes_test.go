package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestProjectExportImportFork(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "export-src")

	rec := doJSON(t, s, http.MethodPost, "/memory", alice, map[string]any{
		"project_id": proj,
		"key":        "export/fact",
		"content":    "this memory is long enough to export and reimport later",
		"level":      "project",
		"scope":      "fact",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create memory: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj+"/export?format=json", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export json: %d %s", rec.Code, rec.Body.String())
	}
	var doc exportDoc
	decodeBody(t, rec, &doc)
	if doc.Version != 1 || len(doc.Memories) != 1 || doc.Memories[0].Key != "export/fact" {
		t.Fatalf("export doc = %+v", doc)
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj+"/export?format=markdown", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export md: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "## `export/fact`") {
		t.Fatalf("markdown body: %s", rec.Body.String())
	}

	dest := resolveTestProject(t, s, alice, "export-dst")
	rec = doJSON(t, s, http.MethodPost, "/projects/"+dest+"/import", alice, map[string]any{
		"memories": []map[string]any{
			{
				"key":     "imported/fact",
				"content": "imported memory content that is definitely long enough",
				"level":   "project",
				"scope":   "fact",
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var imp importResult
	decodeBody(t, rec, &imp)
	if imp.Imported != 1 {
		t.Fatalf("import result = %+v", imp)
	}

	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/fork", alice, map[string]any{
		"folder_name": "export-forked",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", rec.Code, rec.Body.String())
	}
	var fork map[string]any
	decodeBody(t, rec, &fork)
	if fork["memories_copied"] != float64(1) {
		t.Fatalf("fork = %+v", fork)
	}
	projObj, _ := fork["project"].(map[string]any)
	if projObj == nil || projObj["id"] == "" {
		t.Fatalf("fork project missing: %+v", fork)
	}
}
