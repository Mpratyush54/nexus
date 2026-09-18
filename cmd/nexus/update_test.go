package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestMatchArtifact(t *testing.T) {
	arts := []store.ReleaseArtifact{
		{OS: "linux", Arch: "amd64", URL: "https://ex/linux"},
		{OS: "windows", Arch: "amd64", URL: "https://ex/win.exe"},
	}
	got := matchArtifact(arts, "windows", "amd64")
	if got == nil || got.URL != "https://ex/win.exe" {
		t.Fatalf("got %+v", got)
	}
	if matchArtifact(arts, "darwin", "arm64") != nil {
		t.Fatal("expected no darwin artifact")
	}
}

func TestFetchLatestCLI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("app") != "cli" {
			t.Errorf("app = %q", r.URL.Query().Get("app"))
		}
		_ = json.NewEncoder(w).Encode(store.AppRelease{
			App:     "cli",
			Version: "0.2.0",
			Channel: "stable",
			Artifacts: []store.ReleaseArtifact{
				{OS: "linux", Arch: "amd64", URL: "https://ex/nexus"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rel, err := fetchLatestCLI(context.Background(), Config{ServerURL: srv.URL}, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.2.0" {
		t.Fatalf("version = %s", rel.Version)
	}
}

func TestRunVersionCheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(store.AppRelease{App: "cli", Version: "9.9.9", Channel: "stable"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var out strings.Builder
	if err := runVersion(context.Background(), Config{ServerURL: srv.URL}, []string{"--check"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Fatalf("output = %s", out.String())
	}
}
