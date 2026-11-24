package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/core"
)

func TestRealDemoHandoffPackagesAcceptsAndRestores(t *testing.T) {
	workspace, err := filepath.Abs("workspace")
	if err != nil {
		t.Fatal(err)
	}
	srv, st, err := seed(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	st.URL = httpServer.URL
	stateDir := t.TempDir()
	if err = configs(stateDir, st); err != nil {
		t.Fatal(err)
	}
	recipientCloud := &core.HTTPCloud{BaseURL: st.URL, Token: st.RecipientToken}
	if _, err = recipientCloud.Do(t.Context(), http.MethodGet, "/v1/agent-sessions/"+st.SessionID, nil); err == nil {
		t.Fatal("private conversation visible before acceptance")
	}
	for _, action := range []string{"send", "accept", "restore"} {
		if err = flow(t.Context(), stateDir, action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if _, err = recipientCloud.Do(t.Context(), http.MethodGet, "/v1/agent-sessions/"+st.SessionID+"/fetch?limit=80", nil); err != nil {
		t.Fatalf("accepted recipient cannot read chat: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved demoState
	if err = json.Unmarshal(raw, &saved); err != nil || saved.TeleportID == "" {
		t.Fatal("missing actual transfer state", err)
	}
	if err = flow(t.Context(), stateDir, "restore"); err == nil {
		t.Fatal("second restore overwrote existing directory")
	}
}
