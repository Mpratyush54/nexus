package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) billingStore() (store.BillingStore, bool) {
	bs, ok := s.Store.(store.BillingStore)
	return bs, ok
}

func (s *Server) registerBillingRoutes() {
	s.Mux.HandleFunc("GET /billing/plans", s.handleBillingPlans)
	s.Mux.HandleFunc("GET /billing/subscription", s.requireAuth(s.handleBillingMeGet))
	s.Mux.HandleFunc("POST /billing/subscription", s.requireAuth(s.handleBillingMeSet))
	s.Mux.HandleFunc("GET /orgs/{id}/billing", s.requireAuth(s.handleOrgBillingGet))
	s.Mux.HandleFunc("POST /orgs/{id}/billing", s.requireAuth(s.handleOrgBillingSet))
	s.Mux.HandleFunc("GET /admin/subscriptions", s.requireAuth(s.requirePlatformAdmin(s.handleAdminSubscriptions)))
	s.Mux.HandleFunc("PUT /admin/subscriptions", s.requireAuth(s.requirePlatformAdmin(s.handleAdminSubscriptionPut)))
}

type billingSetRequest struct {
	PlanID string `json:"plan_id"`
}

type adminSubPutRequest struct {
	OwnerType string `json:"owner_type"`
	OwnerID   string `json:"owner_id"`
	PlanID    string `json:"plan_id"`
}

func (s *Server) handleBillingPlans(w http.ResponseWriter, r *http.Request) {
	bs, ok := s.billingStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": []*store.Plan{}, "count": 0})
		return
	}
	items, err := bs.ListPlans(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list plans: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.Plan{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleBillingMeGet(w http.ResponseWriter, r *http.Request) {
	s.writeBillingSnapshot(w, r, store.OwnerUser, authSubject(r))
}

func (s *Server) handleBillingMeSet(w http.ResponseWriter, r *http.Request) {
	var req billingSetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyBillingPlan(w, r, store.OwnerUser, authSubject(r), req.PlanID)
}

func (s *Server) handleOrgBillingGet(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgMember(w, r, os, id) {
		return
	}
	s.writeBillingSnapshot(w, r, store.OwnerOrg, id)
}

func (s *Server) handleOrgBillingSet(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	var req billingSetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyBillingPlan(w, r, store.OwnerOrg, id, req.PlanID)
}

func (s *Server) handleAdminSubscriptions(w http.ResponseWriter, r *http.Request) {
	bs, ok := s.billingStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": []*store.Subscription{}, "count": 0})
		return
	}
	items, err := bs.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list subscriptions: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.Subscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleAdminSubscriptionPut(w http.ResponseWriter, r *http.Request) {
	var req adminSubPutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.OwnerID) == "" || strings.TrimSpace(req.PlanID) == "" {
		writeError(w, http.StatusBadRequest, "owner_id and plan_id are required")
		return
	}
	s.applyBillingPlan(w, r, req.OwnerType, req.OwnerID, req.PlanID)
}

func (s *Server) applyBillingPlan(w http.ResponseWriter, r *http.Request, ownerType, ownerID, planID string) {
	bs, ok := s.billingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "billing not supported by configured store")
		return
	}
	sub, err := bs.SetSubscriptionPlan(r.Context(), ownerType, ownerID, planID, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeBillingFrom(w, r, bs, sub)
}

func (s *Server) writeBillingSnapshot(w http.ResponseWriter, r *http.Request, ownerType, ownerID string) {
	bs, ok := s.billingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "billing not supported by configured store")
		return
	}
	sub, err := bs.EnsureSubscription(r.Context(), ownerType, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load subscription: "+err.Error())
		return
	}
	s.writeBillingFrom(w, r, bs, sub)
}

func (s *Server) writeBillingFrom(w http.ResponseWriter, r *http.Request, bs store.BillingStore, sub *store.Subscription) {
	plan, err := bs.GetPlan(r.Context(), sub.PlanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load plan: "+err.Error())
		return
	}
	usage, err := bs.CountBillingUsage(r.Context(), sub.OwnerType, sub.OwnerID)
	if err != nil {
		usage = &store.BillingUsage{}
	}
	provider := store.ProviderManual
	if strings.TrimSpace(os.Getenv("STRIPE_SECRET")) != "" || strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")) != "" {
		provider = store.ProviderStripe
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscription": sub,
		"plan":         plan,
		"usage":        usage,
		"checkout": map[string]any{
			"provider": provider,
			"url":      nil,
		},
	})
}

func (s *Server) billingOwnerForProject(ctx context.Context, projectID string) (ownerType, ownerID string) {
	p, err := s.Store.GetProject(ctx, projectID)
	if err != nil || p == nil {
		return store.OwnerUser, ""
	}
	if strings.TrimSpace(p.OrgID) != "" {
		return store.OwnerOrg, p.OrgID
	}
	if strings.TrimSpace(p.CreatedBy) != "" {
		return store.OwnerUser, p.CreatedBy
	}
	return store.OwnerUser, ""
}

// enforcePlanDimension refuses the request with 402 when the owner's plan
// is at capacity (numeric) or missing a feature flag.
func (s *Server) enforcePlanDimension(w http.ResponseWriter, r *http.Request, ownerType, ownerID, dim string) bool {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return true
	}
	bs, ok := s.billingStore()
	if !ok {
		return true
	}
	sub, err := bs.EnsureSubscription(r.Context(), ownerType, ownerID)
	if err != nil {
		return true
	}
	plan, err := bs.GetPlan(r.Context(), sub.PlanID)
	if err != nil || plan == nil {
		return true
	}
	if dim == "github_import" {
		if plan.Limits.GithubImport {
			return true
		}
		writeError(w, http.StatusPaymentRequired, "GitHub import is not included on the "+plan.Name+" plan")
		return false
	}
	usage, err := bs.CountBillingUsage(r.Context(), ownerType, ownerID)
	if err != nil || usage == nil {
		return true
	}
	var cap, used int
	switch dim {
	case "orgs":
		cap, used = plan.Limits.Orgs, usage.Orgs
	case "projects":
		cap, used = plan.Limits.Projects, usage.Projects
	case "members":
		cap, used = plan.Limits.Members, usage.Members
	case "memories":
		cap, used = plan.Limits.Memories, usage.Memories
	default:
		return true
	}
	if !store.LimitReached(cap, used) {
		return true
	}
	writeError(w, http.StatusPaymentRequired, "plan limit reached: "+dim+" ("+strconv.Itoa(used)+"/"+strconv.Itoa(cap)+") on "+plan.Name+" — upgrade to continue")
	return false
}
