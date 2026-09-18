package server

import (
	"net/http"
	"testing"
)

func TestBillingPlansPublicAndPersonalUpgrade(t *testing.T) {
	s := newTestServer()

	rec := doJSON(t, s, http.MethodGet, "/billing/plans", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("plans = %d %s", rec.Code, rec.Body.String())
	}
	var catalog struct {
		Count int `json:"count"`
		Items []struct {
			ID         string `json:"id"`
			PriceCents int    `json:"price_cents"`
		} `json:"items"`
	}
	decodeBody(t, rec, &catalog)
	if catalog.Count != 3 {
		t.Fatalf("want 3 plans, got %+v", catalog)
	}

	alice := loginAs(t, s, "alice")
	rec = doJSON(t, s, http.MethodGet, "/billing/subscription", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me sub = %d %s", rec.Code, rec.Body.String())
	}
	var snap struct {
		Plan struct {
			ID string `json:"id"`
		} `json:"plan"`
		Subscription struct {
			Status   string `json:"status"`
			Provider string `json:"provider"`
		} `json:"subscription"`
		Checkout struct {
			Provider string `json:"provider"`
		} `json:"checkout"`
	}
	decodeBody(t, rec, &snap)
	if snap.Plan.ID != "free" || snap.Subscription.Status != "active" || snap.Subscription.Provider != "manual" {
		t.Fatalf("default snap = %+v", snap)
	}

	rec = doJSON(t, s, http.MethodPost, "/billing/subscription", alice, map[string]any{"plan_id": "pro"})
	if rec.Code != http.StatusOK {
		t.Fatalf("upgrade = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &snap)
	if snap.Plan.ID != "pro" {
		t.Fatalf("upgraded = %+v", snap)
	}
}

func TestBillingOrgPlanAndLimit(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodPost, "/orgs", alice, map[string]any{"name": "Acme", "slug": "acme-bill"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("org1 = %d %s", rec.Code, rec.Body.String())
	}
	var org struct {
		ID string `json:"id"`
	}
	decodeBody(t, rec, &org)

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/billing", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("org billing = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs", alice, map[string]any{"name": "Beta"})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("second org on free = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/billing/subscription", alice, map[string]any{"plan_id": "pro"})
	if rec.Code != http.StatusOK {
		t.Fatalf("upgrade user = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs", alice, map[string]any{"name": "Beta"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("second org on pro = %d %s", rec.Code, rec.Body.String())
	}

	t.Setenv("PLATFORM_ADMIN_USERNAMES", "alice")
	rec = doJSON(t, s, http.MethodPut, "/admin/subscriptions", alice, map[string]any{
		"owner_type": "org",
		"owner_id":   org.ID,
		"plan_id":    "team",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin assign = %d %s", rec.Code, rec.Body.String())
	}
	var assigned struct {
		Plan struct {
			ID string `json:"id"`
		} `json:"plan"`
	}
	decodeBody(t, rec, &assigned)
	if assigned.Plan.ID != "team" {
		t.Fatalf("assigned = %+v", assigned)
	}
}
