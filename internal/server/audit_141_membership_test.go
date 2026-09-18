package server

// Project-membership boundary tests (issue #141): authenticated is not
// authorized — cross-project reads and writes 403 while same-project
// member access keeps working.

import (
	"net/http"
	"testing"
)

func TestCrossProjectMemoryForbidden(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	projA := resolveTestProject(t, s, alice, "mship-a") // alice member
	projB := resolveTestProject(t, s, bob, "mship-b")   // bob member, alice not

	// Bob reads Alice's project: 403.
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id="+projA+"&q=x", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project search = %d, want 403", rec.Code)
	}
	// Bob writes to Alice's project: 403.
	rec = doJSON(t, s, http.MethodPost, "/memory", bob, map[string]any{
		"project_id": projA, "key": "evil/k",
		"content": "attacker content planted across projects here",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project write = %d, want 403", rec.Code)
	}
	// Alice reads her own project: 200.
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+projA+"&q=x", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-project search = %d, want 200", rec.Code)
	}
	_ = projB
}

func TestCrossProjectEpisodeAndSessionForbidden(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	projA := resolveTestProject(t, s, alice, "mship-ep-a")
	resolveTestProject(t, s, bob, "mship-ep-b")

	rec := doJSON(t, s, http.MethodGet, "/episodes/search?project_id="+projA, bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project episode search = %d, want 403", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions?project_id="+projA, bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project session list = %d, want 403", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/branches?project_id="+projA, bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project branch list = %d, want 403", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+projA+"/active", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project workspace active = %d, want 403", rec.Code)
	}
}

func TestWorkspaceRegisterForcesSelf(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "mship-self")
	// A spoofed user_id is ignored: attribution is the JWT subject.
	rec := doJSON(t, s, http.MethodPost, "/workspaces/register", alice, map[string]string{
		"project_id": proj, "machine_id": "m-spoof", "path": "/tmp/spoof",
		"user_id": "mallory",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ws struct {
		UserID string `json:"user_id"`
	}
	decodeBody(t, rec, &ws)
	if ws.UserID == "mallory" {
		t.Fatal("spoofed user_id accepted on register")
	}
	if ws.UserID != "alice" {
		t.Fatalf("register user = %q, want alice (JWT subject)", ws.UserID)
	}
}
