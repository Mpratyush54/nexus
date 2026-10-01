package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestMemStoreBillingCatalogAndUpgrade(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	plans, err := s.ListPlans(ctx, true)
	if err != nil || len(plans) != 3 {
		t.Fatalf("plans = %d err=%v", len(plans), err)
	}
	if plans[0].ID != PlanFree || plans[1].ID != PlanPro || plans[2].ID != PlanTeam {
		t.Fatalf("order = %+v", plans)
	}

	sub, err := s.EnsureSubscription(ctx, OwnerUser, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if sub.PlanID != PlanFree || sub.Status != SubActive || sub.Provider != ProviderManual {
		t.Fatalf("default sub = %+v", sub)
	}
	again, err := s.EnsureSubscription(ctx, OwnerUser, "alice")
	if err != nil || again.ID != sub.ID {
		t.Fatalf("ensure should be idempotent: %+v %v", again, err)
	}

	up, err := s.SetSubscriptionPlan(ctx, OwnerUser, "alice", PlanPro, "alice")
	if err != nil || up.PlanID != PlanPro {
		t.Fatalf("upgrade = %+v %v", up, err)
	}

	if _, err := s.SetSubscriptionPlan(ctx, OwnerUser, "alice", "nope", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown plan: %v", err)
	}
}

func TestMemStoreBillingUsageCountsOrgs(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	org, err := s.CreateOrganization(ctx, "Acme", "acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CountBillingUsage(ctx, OwnerUser, "alice")
	if err != nil || u.Orgs != 1 {
		t.Fatalf("user usage = %+v %v", u, err)
	}
	ou, err := s.CountBillingUsage(ctx, OwnerOrg, org.ID)
	if err != nil || ou.Orgs != 1 || ou.Members < 1 {
		t.Fatalf("org usage = %+v %v", ou, err)
	}
}

func TestLimitReachedZeroIsUnlimited(t *testing.T) {
	if LimitReached(0, 999) {
		t.Fatal("0 cap must be unlimited")
	}
	if !LimitReached(1, 1) {
		t.Fatal("1/1 is reached")
	}
	if LimitReached(5, 4) {
		t.Fatal("4/5 is not reached")
	}
}

func TestDefaultPlansIncludeStorageBytes(t *testing.T) {
	plans := DefaultPlans()
	want := map[string]int64{
		PlanFree: 5 << 30,
		PlanPro:  100 << 30,
		PlanTeam: 250 << 30,
	}
	for _, p := range plans {
		if p.Limits.StorageBytes != want[p.ID] {
			t.Fatalf("%s storage = %d want %d", p.ID, p.Limits.StorageBytes, want[p.ID])
		}
	}
	if PlanStorageBytes(PlanFree) != 5<<30 || PlanStorageBytes(PlanPro) != 100<<30 {
		t.Fatal("PlanStorageBytes mismatch")
	}
}

func TestStorageUsageAppliesPlanCapWhenMissing(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	used, cap, err := s.StorageUsage(ctx, "user:alice")
	if err != nil || used != 0 || cap != 5<<30 {
		t.Fatalf("free default = used=%d cap=%d err=%v", used, cap, err)
	}
	if _, err := s.EnsureSubscription(ctx, OwnerUser, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSubscriptionPlan(ctx, OwnerUser, "alice", PlanPro, "alice"); err != nil {
		t.Fatal(err)
	}
	_, cap, err = s.StorageUsage(ctx, "user:alice")
	if err != nil || cap != 100<<30 {
		t.Fatalf("pro cap = %d err=%v", cap, err)
	}
}

func TestFreePlanAtCapAllowsTurnsRejectsFileBlobs(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	proj, err := s.ResolveProject(ctx, "", "", "cap-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, proj.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureSubscription(ctx, OwnerUser, "alice"); err != nil {
		t.Fatal(err)
	}
	cap := PlanStorageBytes(PlanFree)
	if err := s.SetStorageUsage(ctx, "user:alice", cap, cap); err != nil {
		t.Fatal(err)
	}
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "alice", Harness: "claude",
		NativeID: "ses_cap", OriginMachineID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: 1, Role: "user", TextPreview: "hi"}); err != nil {
		t.Fatalf("turn at cap: %v", err)
	}
	body := []byte("file-bytes")
	sum := sha256Hex(body)
	if err := s.PutBlob(ctx, proj.ID, sum, "plain", "file", body); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("file blob: %v", err)
	}
	tr := []byte(`{"ok":true}`)
	trSum := sha256Hex(tr)
	if err := s.PutBlob(ctx, proj.ID, trSum, "plain", "transcript", tr); err != nil {
		t.Fatalf("transcript blob: %v", err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
