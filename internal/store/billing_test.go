package store

import (
	"context"
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
