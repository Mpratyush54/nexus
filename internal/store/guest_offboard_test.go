package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuestOffboardTransfersOrgSessionsOnly(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	org, err := m.CreateOrganization(ctx, "Acme", "acme-off", "owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.CreateOrganization(ctx, "Other", "other-off", "owner")
	if err != nil {
		t.Fatal(err)
	}
	orgProj, err := m.CreateOrgProject(ctx, org.ID, "org-repo", "Org", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherProj, err := m.CreateOrgProject(ctx, other.ID, "other-repo", "Other", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	personal, err := m.ResolveProject(ctx, "", "", "personal-repo")
	if err != nil {
		t.Fatal(err)
	}

	const title = "SECRET-TITLE"
	const summary = "SECRET-SUMMARY"
	orgSess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: orgProj.ID, OwnerUserID: "leaver", Harness: "claude", NativeID: "ses_org",
		Title: title, Summary: summary, Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	teamSess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: orgProj.ID, OwnerUserID: "leaver", Harness: "codex", NativeID: "ses_team",
		Title: "team-title", Visibility: "team",
	})
	if err != nil {
		t.Fatal(err)
	}
	personalSess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: personal.ID, OwnerUserID: "leaver", Harness: "claude", NativeID: "ses_personal",
		Title: title, Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherSess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: otherProj.ID, OwnerUserID: "leaver", Harness: "claude", NativeID: "ses_other",
		Title: title, Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.GrantAgentSession(ctx, orgSess.ID, "kept-user", "leaver"); err != nil {
		t.Fatal(err)
	}

	res, err := m.Offboard(ctx, org.ID, "leaver", "receiver")
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsTransferred != 2 || res.GrantsKept != 1 {
		t.Fatalf("result = %+v, want 2 transferred and 1 grant kept", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), title) || strings.Contains(string(raw), summary) || strings.Contains(string(raw), "team-title") {
		t.Fatalf("offboard result leaked content: %s", raw)
	}

	got, err := m.GetAgentSession(ctx, orgSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerUserID != "receiver" || got.Visibility != "private" || got.Title != title {
		t.Fatalf("org session after offboard: %+v", got)
	}
	gotTeam, err := m.GetAgentSession(ctx, teamSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTeam.OwnerUserID != "receiver" || gotTeam.Visibility != "team" {
		t.Fatalf("team session visibility changed: %+v", gotTeam)
	}
	gotPersonal, err := m.GetAgentSession(ctx, personalSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPersonal.OwnerUserID != "leaver" {
		t.Fatalf("personal session transferred: %+v", gotPersonal)
	}
	gotOther, err := m.GetAgentSession(ctx, otherSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotOther.OwnerUserID != "leaver" {
		t.Fatalf("other org session transferred: %+v", gotOther)
	}
	for _, id := range []string{"receiver", "kept-user"} {
		ok, err := m.CanReadAgentSession(ctx, id, orgSess.ID)
		if err != nil || !ok {
			t.Fatalf("CanRead %s: %v %v", id, ok, err)
		}
	}
	ok, err := m.CanReadAgentSession(ctx, "leaver", orgSess.ID)
	if err != nil || ok {
		t.Fatalf("leaver still reads transferred session: %v %v", ok, err)
	}
	ok, err = m.CanReadAgentSession(ctx, "org-admin", orgSess.ID)
	if err != nil || ok {
		t.Fatalf("admin reads private session: %v %v", ok, err)
	}
}

func TestGuestOffboardLinks(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	proj, err := m.ResolveProject(ctx, "", "", "guest-repo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "owner", Harness: "claude", NativeID: "ses_guest",
		Title: "SECRET-TITLE", Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateGuestLink(ctx, sess.ID, "stranger", time.Time{}, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner create: %v", err)
	}
	link, err := m.CreateGuestLink(ctx, sess.ID, "owner", time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if link.Token == "" || !link.SingleUse {
		t.Fatalf("link: %+v", link)
	}
	until := time.Until(link.ExpiresAt)
	if until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Fatalf("default expiry %s (%s)", link.ExpiresAt, until)
	}
	got, err := m.GetGuestLinkByToken(ctx, link.Token)
	if err != nil || got.SessionID != sess.ID || got.Token != link.Token {
		t.Fatalf("get by token: %+v %v", got, err)
	}
	accepted, err := m.AcceptGuestLink(ctx, link.Token, "guest")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.GranteeUserID != "guest" || accepted.UsedAt == nil {
		t.Fatalf("accepted: %+v", accepted)
	}
	if _, err := m.AcceptGuestLink(ctx, link.Token, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second accept: %v", err)
	}
	ok, err := m.CanReadAgentSession(ctx, "guest", sess.ID)
	if err != nil || !ok {
		t.Fatalf("grantee read: %v %v", ok, err)
	}
	member, err := m.IsProjectMember(ctx, "guest", proj.ID)
	if err != nil || member {
		t.Fatalf("accept added project membership: %v %v", member, err)
	}
	ok, err = m.CanReadAgentSession(ctx, "admin", sess.ID)
	if err != nil || ok {
		t.Fatalf("admin read through guest link: %v %v", ok, err)
	}

	expired, err := m.CreateGuestLink(ctx, sess.ID, "owner", time.Now().Add(-time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptGuestLink(ctx, expired.Token, "guest"); !errors.Is(err, ErrGuestLinkExpired) {
		t.Fatalf("expired accept: %v", err)
	}

	multi, err := m.CreateGuestLink(ctx, sess.ID, "owner", time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptGuestLink(ctx, multi.Token, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptGuestLink(ctx, multi.Token, "bea"); err != nil {
		t.Fatalf("multi-use second accept: %v", err)
	}
	for _, id := range []string{"ada", "bea"} {
		ok, err := m.CanReadAgentSession(ctx, id, sess.ID)
		if err != nil || !ok {
			t.Fatalf("multi grantee %s: %v %v", id, ok, err)
		}
	}
}

func TestGuestOffboardPostgresNonUUID(t *testing.T) {
	ctx := context.Background()
	s := &PostgresStore{}
	if _, err := s.CreateGuestLink(ctx, "not-a-uuid", "owner", time.Now().Add(time.Hour), true); err == nil {
		t.Fatal("create: expected error for non-uuid session")
	}
	if _, err := s.AcceptGuestLink(ctx, "token", "not-a-uuid"); err == nil {
		t.Fatal("accept: expected error for non-uuid user")
	}
	if _, err := s.GetGuestLinkByToken(ctx, ""); err == nil {
		t.Fatal("get: expected error for empty token")
	}
	if _, err := s.Offboard(ctx, "not-a-uuid", "also-no", "still-no"); err == nil {
		t.Fatal("offboard: expected error for non-uuid ids")
	}
}
