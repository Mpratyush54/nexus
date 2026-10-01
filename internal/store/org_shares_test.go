package store

import (
	"testing"
)

func TestListAndRevokeOrgSessionShares(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	org, err := m.CreateOrganization(ctx, "ShareOrg", "share-org", "owner")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := m.CreateOrgProject(ctx, org.ID, "p", "P", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "owner", Harness: "claude",
		NativeID: "ses_org_share", OriginMachineID: "m1", Title: "secret-title",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.GrantAgentSessionOpts(ctx, sess.ID, "member", "owner", SessionGrantOpts{Live: true}); err != nil {
		t.Fatal(err)
	}
	items, err := m.ListOrgSessionShares(ctx, org.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list=%+v err=%v", items, err)
	}
	if items[0].SessionID != sess.ID || items[0].GranteeID != "member" || items[0].OwnerID != "owner" {
		t.Fatalf("row=%+v", items[0])
	}
	if err := m.RevokeOrgSessionShare(ctx, org.ID, sess.ID, "member"); err != nil {
		t.Fatal(err)
	}
	items, err = m.ListOrgSessionShares(ctx, org.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("after revoke=%+v err=%v", items, err)
	}
}
