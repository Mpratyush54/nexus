package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestRegressionKeywordFallbackSecondPage(t *testing.T) {
	s := newTestServer()
	s.Embedder = func(context.Context, string) ([]float32, error) {
		v := make([]float32, store.EmbeddingDim)
		v[0] = 1
		return v, nil
	}
	tok := loginAs(t, s, "alice")
	project := resolveTestProject(t, s, tok, "review-keyword-pages")
	for i := 0; i < 3; i++ {
		item := &store.MemoryItem{ProjectID: project, Key: fmt.Sprintf("needle-%d", i), Content: "needle fixture content for keyword pagination", Level: store.LevelProject, Scope: "fact", Status: store.StatusConfirmed, Confidence: 0.9}
		if err := s.Store.CreateMemoryItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < 2; offset++ {
		rec := doJSON(t, s, http.MethodGet, fmt.Sprintf("/memory/search?project_id=%s&q=needle&limit=1&offset=%d", project, offset), tok, nil)
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var page struct {
			Items []store.MemoryItem `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		t.Logf("offset=%d response=%s", offset, rec.Body.String())
		if len(page.Items) != 1 {
			t.Errorf("offset=%d: got %d items, want 1", offset, len(page.Items))
		}
	}
}

func TestRegressionHiddenVectorResultStopsPagination(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	project := resolveTestProject(t, s, tok, "review-vector-visibility")
	for i := 0; i < 3; i++ {
		vec := make([]float32, store.EmbeddingDim)
		vec[0] = 1
		vec[1] = float32(i) / 10
		vis, user := store.VisibilityProject, "alice"
		if i == 0 {
			vis, user = store.VisibilityPrivate, "bob"
		}
		item := &store.MemoryItem{ProjectID: project, Key: fmt.Sprintf("vec-%d", i), Content: "vector fixture content for readable pagination", UserID: user, Visibility: vis, Level: store.LevelProject, Scope: "fact", Status: store.StatusConfirmed, Confidence: 0.9, Embedding: vec}
		if err := s.Store.CreateMemoryItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	vec := make([]string, store.EmbeddingDim)
	for i := range vec {
		vec[i] = "0"
	}
	vec[0] = "1"
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id="+project+"&limit=2&embedding="+strings.Join(vec, ","), tok, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var page struct {
		Items   []store.MemoryItem `json:"items"`
		HasMore bool               `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	t.Logf("visible items=%d has_more=%v; expected two visible items or another page", len(page.Items), page.HasMore)
	if len(page.Items) < 2 && !page.HasMore {
		t.Errorf("stopped after %d visible items; two readable items exist", len(page.Items))
	}
}

func TestRegressionPinnedGrantTimelineLeaksLaterText(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	owner := loginAs(t, s, "owner")
	grantee := loginAs(t, s, "grantee")
	project := resolveTestProject(t, s, owner, "review-pinned-search")
	row, err := mem.UpsertAgentSession(t.Context(), &store.AgentSession{ProjectID: project, OwnerUserID: "owner", Harness: "codex", NativeID: "review-pinned", OriginMachineID: "lap", Title: "Shared chat"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := mem.CreateSessionVersion(t.Context(), row.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("pinned share contents")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := mem.PutBlob(t.Context(), project, hash, "plain", "transcript", body); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := mem.CompleteSessionVersion(t.Context(), row.ID, version.Version, manifest); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantAgentSessionOpts(t.Context(), row.ID, "grantee", "owner", store.SessionGrantOpts{VersionID: version.ID}); err != nil {
		t.Fatal(err)
	}
	if err := mem.AppendSessionTurn(t.Context(), store.SessionTurn{SessionID: row.ID, Idx: 1, Role: "assistant", TextPreview: "private-after-sharing-secret"}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+row.ID+"/turns", grantee, nil)
	if rec.Code != 403 {
		t.Fatalf("control live endpoint=%d want403 body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline?q=private-after-sharing-secret", grantee, nil)
	t.Logf("timeline response=%s", rec.Body.String())
	if rec.Code != 200 {
		t.Fatalf("timeline=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-after-sharing-secret") {
		t.Error("point-in-time recipient can search and read unshared later text")
	}
	row.Summary = "private-after-sharing-secret"
	if _, err := mem.UpsertAgentSession(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/agent-sessions/" + row.ID, "/v1/agent-sessions/" + row.ID + "/summary", "/v1/timeline"} {
		rec = doJSON(t, s, http.MethodGet, path, grantee, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "private-after-sharing-secret") {
			t.Fatalf("pinned metadata leaked live summary: %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// Being a project teammate does not override a private snapshot pin.
	if err := mem.GrantMember(t.Context(), project, "grantee", "owner"); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline?q=private-after-sharing-secret", grantee, nil)
	if strings.Contains(rec.Body.String(), "private-after-sharing-secret") {
		t.Error("private session leaked to project teammate")
	}

	// Explicit team visibility does grant live access, even if a separate
	// snapshot-only grant also exists for the same teammate.
	if err := mem.SetAgentSessionVisibility(t.Context(), row.ID, "team"); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline?q=private-after-sharing-secret", grantee, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "private-after-sharing-secret") {
		t.Fatalf("team live search blocked: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+row.ID+"/turns", grantee, nil)
	if rec.Code != 200 {
		t.Fatalf("team live turns blocked: %d %s", rec.Code, rec.Body.String())
	}
}
