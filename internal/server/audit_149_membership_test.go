package server

// Membership bootstrap tests (issue #149): resolve claims the creator,
// registration requires membership (never creates it), server fields are
// stripped, heartbeat is owner-only, grants onboard new users.

import (
	"net/http"
	"testing"
)

func TestResolveClaimsCreator(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "claim-proj")
	got, err := s.Store.GetProject(t.Context(), proj)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedBy != "alice" {
		t.Fatalf("CreatedBy = %q, want alice (resolve claims)", got.CreatedBy)
	}
	// A second resolver does not steal creatorness.
	bob := loginAs(t, s, "bob")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", bob, map[string]string{
		"folder_name": "claim-proj",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d", rec.Code)
	}
	got, _ = s.Store.GetProject(t.Context(), proj)
	if got.CreatedBy != "alice" {
		t.Fatalf("CreatedBy = %q after bob resolve, want alice", got.CreatedBy)
	}
	// Bob is not a member through resolving.
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj+"&q=x", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob search = %d, want 403", rec.Code)
	}
}

func TestRegisterRequiresMembership(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	proj := resolveTestProject(t, s, alice, "reg-guard-proj")
	// Bob (non-member) cannot register a workspace to bootstrap in.
	rec := doJSON(t, s, http.MethodPost, "/workspaces/register", bob, map[string]string{
		"project_id": proj, "machine_id": "m-bob", "path": "/tmp/bob",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member register = %d, want 403", rec.Code)
	}
	// Alice grants bob; now he can.
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/workspaces/register", bob, map[string]string{
		"project_id": proj, "machine_id": "m-bob", "path": "/tmp/bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("member register = %d, body = %s", rec.Code, rec.Body.String())
	}
	// Non-member cannot grant either.
	mallory := loginAs(t, s, "mallory")
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", mallory, map[string]string{
		"user_id": "mallory",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member grant = %d, want 403", rec.Code)
	}
}

func TestRegisterStripsDesignation(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "desig-proj")
	rec := doJSON(t, s, http.MethodPost, "/workspaces/register", alice, map[string]any{
		"project_id": proj, "machine_id": "m-desig", "path": "/tmp/desig",
		"is_designated_processor": true, "is_online": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d", rec.Code)
	}
	var ws struct {
		IsDesignatedProcessor bool   `json:"is_designated_processor"`
		IsOnline              bool   `json:"is_online"`
		ID                    string `json:"id"`
	}
	decodeBody(t, rec, &ws)
	if ws.IsDesignatedProcessor {
		t.Fatal("client-supplied designation accepted at registration")
	}
	if !ws.IsOnline {
		t.Fatal("server must mark fresh registrations online")
	}
}

func TestHeartbeatOwnerOnly(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	proj := resolveTestProject(t, s, alice, "hb-own-proj")
	// Alice's workspace id via active lookup.
	rec := doJSON(t, s, http.MethodGet, "/workspaces/"+proj+"/active", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("active = %d", rec.Code)
	}
	var active struct {
		ID string `json:"id"`
	}
	decodeBody(t, rec, &active)
	// Bob heartbeats Alice's workspace: 403 or 404, never 200.
	rec = doJSON(t, s, http.MethodPost, "/workspaces/heartbeat", bob, map[string]any{
		"workspace_id": active.ID, "branch": "evil",
	})
	if rec.Code == http.StatusOK {
		t.Fatal("cross-user heartbeat succeeded")
	}
	// Alice heartbeats her own: 200.
	rec = doJSON(t, s, http.MethodPost, "/workspaces/heartbeat", alice, map[string]any{
		"workspace_id": active.ID, "branch": "main",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("own heartbeat = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreatorCannotBeRevoked(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "revoke-proj")
	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant = %d", rec.Code)
	}
	// Revoking the creator conflicts.
	rec = doJSON(t, s, http.MethodDelete, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "alice",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("revoke creator = %d, want 409", rec.Code)
	}
	// Revoking a grant works; bob loses access.
	rec = doJSON(t, s, http.MethodDelete, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke grant = %d", rec.Code)
	}
	bob := loginAs(t, s, "bob")
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj+"&q=x", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoked search = %d, want 403", rec.Code)
	}
}
