package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	PlanFree = "free"
	PlanPro  = "pro"
	PlanTeam = "team"

	OwnerUser = "user"
	OwnerOrg  = "org"

	SubActive     = "active"
	SubTrialing   = "trialing"
	SubPastDue    = "past_due"
	SubCanceled   = "canceled"
	SubIncomplete = "incomplete"

	ProviderManual = "manual"
	ProviderStripe = "stripe"

	BillingPeriod = 30 * 24 * time.Hour
)

// ErrPlanLimit is returned when a subscription cap would be exceeded.
var ErrPlanLimit = errors.New("plan limit reached")

// PlanLimits are numeric caps. Zero on a numeric field means unlimited.
type PlanLimits struct {
	Orgs         int  `json:"orgs"`
	Projects     int  `json:"projects"`
	Members      int  `json:"members"`
	Memories     int  `json:"memories"`
	GithubImport bool `json:"github_import"`
}

// Plan is a public catalog row.
type Plan struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	PriceCents  int        `json:"price_cents"`
	Currency    string     `json:"currency"`
	Interval    string     `json:"interval"`
	Public      bool       `json:"public"`
	Rank        int        `json:"rank"`
	Limits      PlanLimits `json:"limits"`
	Features    []string   `json:"features,omitempty"`
}

// Subscription binds an owner (user or org) to a plan.
// Provider/ProviderRef are the Stripe (or other) hook for later checkout.
type Subscription struct {
	ID                 string    `json:"id"`
	OwnerType          string    `json:"owner_type"`
	OwnerID            string    `json:"owner_id"`
	PlanID             string    `json:"plan_id"`
	Status             string    `json:"status"`
	Provider           string    `json:"provider"`
	ProviderRef        string    `json:"provider_ref,omitempty"`
	CurrentPeriodStart time.Time `json:"current_period_start"`
	CurrentPeriodEnd   time.Time `json:"current_period_end"`
	CancelAtPeriodEnd  bool      `json:"cancel_at_period_end,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// BillingUsage is current consumption against a subscription.
type BillingUsage struct {
	Orgs     int `json:"orgs"`
	Projects int `json:"projects"`
	Members  int `json:"members"`
	Memories int `json:"memories"`
}

// BillingStore is the catalog + subscription surface.
type BillingStore interface {
	ListPlans(ctx context.Context, publicOnly bool) ([]*Plan, error)
	GetPlan(ctx context.Context, id string) (*Plan, error)
	GetSubscription(ctx context.Context, ownerType, ownerID string) (*Subscription, error)
	EnsureSubscription(ctx context.Context, ownerType, ownerID string) (*Subscription, error)
	SetSubscriptionPlan(ctx context.Context, ownerType, ownerID, planID, actor string) (*Subscription, error)
	ListSubscriptions(ctx context.Context) ([]*Subscription, error)
	CountBillingUsage(ctx context.Context, ownerType, ownerID string) (*BillingUsage, error)
}

var (
	_ BillingStore = (*MemStore)(nil)
	_ BillingStore = (*PostgresStore)(nil)
)

func NormalizeOwnerType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case OwnerOrg:
		return OwnerOrg
	default:
		return OwnerUser
	}
}

func NormalizePlanID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func NormalizeSubStatus(st string) string {
	switch strings.ToLower(strings.TrimSpace(st)) {
	case SubTrialing:
		return SubTrialing
	case SubPastDue:
		return SubPastDue
	case SubCanceled:
		return SubCanceled
	case SubIncomplete:
		return SubIncomplete
	default:
		return SubActive
	}
}

func LimitReached(cap, used int) bool {
	if cap <= 0 {
		return false
	}
	return used >= cap
}

func DefaultPlans() []*Plan {
	return []*Plan{
		{
			ID: PlanFree, Name: "Free", Description: "Personal memory for one team.",
			PriceCents: 0, Currency: "usd", Interval: "month", Public: true, Rank: 10,
			Limits:   PlanLimits{Orgs: 1, Projects: 3, Members: 5, Memories: 2000, GithubImport: true},
			Features: []string{"Project memory", "Live presence", "GitHub import"},
		},
		{
			ID: PlanPro, Name: "Pro", Description: "For groups that review and ship together.",
			PriceCents: 1600, Currency: "usd", Interval: "month", Public: true, Rank: 20,
			Limits:   PlanLimits{Orgs: 5, Projects: 25, Members: 25, Memories: 0, GithubImport: true},
			Features: []string{"Everything in Free", "25 seats", "Unlimited memories", "Priority support"},
		},
		{
			ID: PlanTeam, Name: "Team", Description: "Org-wide memory with room to grow.",
			PriceCents: 4800, Currency: "usd", Interval: "month", Public: true, Rank: 30,
			Limits:   PlanLimits{Orgs: 0, Projects: 0, Members: 250, Memories: 0, GithubImport: true},
			Features: []string{"Everything in Pro", "Unlimited orgs & projects", "250 seats", "Admin billing"},
		},
	}
}

func defaultPlanMap() map[string]*Plan {
	out := make(map[string]*Plan, 3)
	for _, p := range DefaultPlans() {
		out[p.ID] = clonePlan(p)
	}
	return out
}

func clonePlan(p *Plan) *Plan {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Features != nil {
		cp.Features = append([]string(nil), p.Features...)
	}
	return &cp
}

func cloneSub(s *Subscription) *Subscription {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func subKey(ownerType, ownerID string) string {
	return NormalizeOwnerType(ownerType) + "/" + strings.TrimSpace(ownerID)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *MemStore) ListPlans(_ context.Context, publicOnly bool) ([]*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Plan, 0, len(s.plans))
	for _, p := range s.plans {
		if publicOnly && !p.Public {
			continue
		}
		out = append(out, clonePlan(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemStore) GetPlan(_ context.Context, id string) (*Plan, error) {
	id = NormalizePlanID(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, fmt.Errorf("store: plan %s: %w", id, ErrNotFound)
	}
	return clonePlan(p), nil
}

func (s *MemStore) GetSubscription(_ context.Context, ownerType, ownerID string) (*Subscription, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("store: owner id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.billingSubs[subKey(ownerType, ownerID)]
	if !ok {
		return nil, fmt.Errorf("store: subscription: %w", ErrNotFound)
	}
	return cloneSub(sub), nil
}

func (s *MemStore) EnsureSubscription(_ context.Context, ownerType, ownerID string) (*Subscription, error) {
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("store: owner id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plans == nil {
		s.plans = defaultPlanMap()
	}
	if s.billingSubs == nil {
		s.billingSubs = map[string]*Subscription{}
	}
	key := subKey(ownerType, ownerID)
	if sub, ok := s.billingSubs[key]; ok {
		return cloneSub(sub), nil
	}
	now := time.Now().UTC()
	sub := &Subscription{
		ID:                 newID("sub"),
		OwnerType:          ownerType,
		OwnerID:            ownerID,
		PlanID:             PlanFree,
		Status:             SubActive,
		Provider:           ProviderManual,
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.Add(BillingPeriod),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	s.billingSubs[key] = sub
	return cloneSub(sub), nil
}

func (s *MemStore) SetSubscriptionPlan(_ context.Context, ownerType, ownerID, planID, actor string) (*Subscription, error) {
	_ = actor
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	planID = NormalizePlanID(planID)
	if ownerID == "" || planID == "" {
		return nil, fmt.Errorf("store: owner and plan are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plans == nil {
		s.plans = defaultPlanMap()
	}
	if s.billingSubs == nil {
		s.billingSubs = map[string]*Subscription{}
	}
	if _, ok := s.plans[planID]; !ok {
		return nil, fmt.Errorf("store: plan %s: %w", planID, ErrNotFound)
	}
	key := subKey(ownerType, ownerID)
	now := time.Now().UTC()
	sub, ok := s.billingSubs[key]
	if !ok {
		sub = &Subscription{
			ID:        newID("sub"),
			OwnerType: ownerType,
			OwnerID:   ownerID,
			Provider:  ProviderManual,
			CreatedAt: now,
		}
	}
	sub.PlanID = planID
	sub.Status = SubActive
	sub.CancelAtPeriodEnd = false
	sub.CurrentPeriodStart = now
	sub.CurrentPeriodEnd = now.Add(BillingPeriod)
	sub.UpdatedAt = now
	if sub.Provider == "" {
		sub.Provider = ProviderManual
	}
	s.billingSubs[key] = sub
	return cloneSub(sub), nil
}

func (s *MemStore) ListSubscriptions(_ context.Context) ([]*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Subscription, 0, len(s.billingSubs))
	for _, sub := range s.billingSubs {
		out = append(out, cloneSub(sub))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OwnerType != out[j].OwnerType {
			return out[i].OwnerType < out[j].OwnerType
		}
		return out[i].OwnerID < out[j].OwnerID
	})
	return out, nil
}

func (s *MemStore) CountBillingUsage(_ context.Context, ownerType, ownerID string) (*BillingUsage, error) {
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := &BillingUsage{}
	if ownerType == OwnerOrg {
		if _, ok := s.orgs[ownerID]; ok {
			u.Orgs = 1
		}
		for _, p := range s.projects {
			if p.OrgID == ownerID {
				u.Projects++
			}
		}
		if mems := s.orgMembers[ownerID]; mems != nil {
			u.Members = len(mems)
		}
		proj := map[string]bool{}
		for _, p := range s.projects {
			if p.OrgID == ownerID {
				proj[p.ID] = true
			}
		}
		for _, m := range s.memories {
			if proj[m.ProjectID] {
				u.Memories++
			}
		}
		return u, nil
	}
	for _, o := range s.orgs {
		if o.CreatedBy == ownerID {
			u.Orgs++
		}
	}
	personal := map[string]bool{}
	for _, p := range s.projects {
		if p.CreatedBy == ownerID && strings.TrimSpace(p.OrgID) == "" {
			u.Projects++
			personal[p.ID] = true
			if granted := s.members[p.ID]; granted != nil {
				u.Members += len(granted)
			} else {
				u.Members++
			}
		}
	}
	for _, m := range s.memories {
		if personal[m.ProjectID] {
			u.Memories++
		}
	}
	return u, nil
}

func (s *PostgresStore) ListPlans(ctx context.Context, publicOnly bool) ([]*Plan, error) {
	q := `SELECT id, name, COALESCE(description, ''), price_cents, currency, interval,
	             public, rank, COALESCE(limits::TEXT, '{}'), COALESCE(features::TEXT, '[]')
	        FROM billing_plans`
	if publicOnly {
		q += " WHERE public = true"
	}
	q += " ORDER BY rank ASC, id ASC"
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list plans: %w", err)
	}
	defer rows.Close()
	var out []*Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetPlan(ctx context.Context, id string) (*Plan, error) {
	id = NormalizePlanID(id)
	row := s.pool.QueryRow(ctx, `SELECT id, name, COALESCE(description, ''), price_cents, currency, interval,
	             public, rank, COALESCE(limits::TEXT, '{}'), COALESCE(features::TEXT, '[]')
	        FROM billing_plans WHERE id = $1`, id)
	p, err := scanPlan(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: plan %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get plan: %w", err)
	}
	return p, nil
}

func scanPlan(row rowScanner) (*Plan, error) {
	var p Plan
	var limitsJSON, featuresJSON string
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Currency, &p.Interval,
		&p.Public, &p.Rank, &limitsJSON, &featuresJSON); err != nil {
		return nil, err
	}
	if limitsJSON != "" && limitsJSON != "null" {
		_ = json.Unmarshal([]byte(limitsJSON), &p.Limits)
	}
	if featuresJSON != "" && featuresJSON != "null" {
		_ = json.Unmarshal([]byte(featuresJSON), &p.Features)
	}
	return &p, nil
}

func (s *PostgresStore) GetSubscription(ctx context.Context, ownerType, ownerID string) (*Subscription, error) {
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	row := s.pool.QueryRow(ctx, `SELECT id::TEXT, owner_type, owner_id, plan_id, status, provider,
	             COALESCE(provider_ref, ''), current_period_start, current_period_end,
	             cancel_at_period_end, created_at, updated_at
	        FROM subscriptions WHERE owner_type = $1 AND owner_id = $2`, ownerType, ownerID)
	sub, err := scanSub(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: subscription: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("store: get subscription: %w", err)
	}
	return sub, nil
}

func scanSub(row rowScanner) (*Subscription, error) {
	var sub Subscription
	if err := row.Scan(&sub.ID, &sub.OwnerType, &sub.OwnerID, &sub.PlanID, &sub.Status, &sub.Provider,
		&sub.ProviderRef, &sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.CancelAtPeriodEnd,
		&sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return nil, err
	}
	return &sub, nil
}

func (s *PostgresStore) EnsureSubscription(ctx context.Context, ownerType, ownerID string) (*Subscription, error) {
	if sub, err := s.GetSubscription(ctx, ownerType, ownerID); err == nil {
		return sub, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	row := s.pool.QueryRow(ctx, `
		INSERT INTO subscriptions (owner_type, owner_id, plan_id, status, provider)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (owner_type, owner_id) DO UPDATE SET updated_at = subscriptions.updated_at
		RETURNING id::TEXT, owner_type, owner_id, plan_id, status, provider,
		          COALESCE(provider_ref, ''), current_period_start, current_period_end,
		          cancel_at_period_end, created_at, updated_at`,
		ownerType, ownerID, PlanFree, SubActive, ProviderManual)
	sub, err := scanSub(row)
	if err != nil {
		return nil, fmt.Errorf("store: ensure subscription: %w", err)
	}
	return sub, nil
}

func (s *PostgresStore) SetSubscriptionPlan(ctx context.Context, ownerType, ownerID, planID, actor string) (*Subscription, error) {
	_ = actor
	if _, err := s.GetPlan(ctx, planID); err != nil {
		return nil, err
	}
	if _, err := s.EnsureSubscription(ctx, ownerType, ownerID); err != nil {
		return nil, err
	}
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	planID = NormalizePlanID(planID)
	row := s.pool.QueryRow(ctx, `
		UPDATE subscriptions SET
		  plan_id = $3,
		  status = $4,
		  cancel_at_period_end = false,
		  current_period_start = now(),
		  current_period_end = now() + interval '30 days',
		  updated_at = now()
		WHERE owner_type = $1 AND owner_id = $2
		RETURNING id::TEXT, owner_type, owner_id, plan_id, status, provider,
		          COALESCE(provider_ref, ''), current_period_start, current_period_end,
		          cancel_at_period_end, created_at, updated_at`,
		ownerType, ownerID, planID, SubActive)
	sub, err := scanSub(row)
	if err != nil {
		return nil, fmt.Errorf("store: set subscription plan: %w", err)
	}
	return sub, nil
}

func (s *PostgresStore) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::TEXT, owner_type, owner_id, plan_id, status, provider,
	             COALESCE(provider_ref, ''), current_period_start, current_period_end,
	             cancel_at_period_end, created_at, updated_at
	        FROM subscriptions ORDER BY owner_type, owner_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountBillingUsage(ctx context.Context, ownerType, ownerID string) (*BillingUsage, error) {
	ownerType = NormalizeOwnerType(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	u := &BillingUsage{}
	if ownerType == OwnerOrg {
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM organizations WHERE id::TEXT = $1`, ownerID).Scan(&u.Orgs)
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE org_id::TEXT = $1`, ownerID).Scan(&u.Projects)
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM organization_members WHERE org_id::TEXT = $1`, ownerID).Scan(&u.Members)
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM memory_items WHERE project_id IN (SELECT id FROM projects WHERE org_id::TEXT = $1)`, ownerID).Scan(&u.Memories)
		return u, nil
	}
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM organizations WHERE created_by::TEXT = $1`, ownerID).Scan(&u.Orgs)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE created_by::TEXT = $1 AND org_id IS NULL`, ownerID).Scan(&u.Projects)
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(n),0) FROM (
		SELECT COUNT(*) AS n FROM project_members pm
		 JOIN projects p ON p.id = pm.project_id
		 WHERE p.created_by::TEXT = $1 AND p.org_id IS NULL
	) t`, ownerID).Scan(&u.Members)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM memory_items m
		JOIN projects p ON p.id = m.project_id
		WHERE p.created_by::TEXT = $1 AND p.org_id IS NULL`, ownerID).Scan(&u.Memories)
	return u, nil
}
