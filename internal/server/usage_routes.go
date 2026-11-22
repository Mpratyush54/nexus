package server

// Token usage and cost for agent chats (migration 048).
//
//	POST /v1/agent-sessions/{id}/usage   {records:[{key?, idx?, model, input, output,
//	                                       cache_read, cache_write, cost_usd?, source_ts?}]}
//	GET  /v1/agent-sessions/{id}/usage   totals by model for one chat
//	GET  /v1/usage?from=&to=&project_id=&group_by=agent|model|day|project
//	               &scope=readable|mine&utc_offset_minutes=
//
// Cost: an agent-reported cost is used as is; otherwise it is estimated from
// the built-in price table (internal/usage) and flagged "estimated". Models
// without a known price show tokens only (unpriced_tokens), never a guess.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
	"central-memory/internal/usage"
)

func (s *Server) registerUsageRoutes() {
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/usage", s.requireAuth(s.handleSessionUsagePost))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/usage", s.requireAuth(s.handleSessionUsageGet))
	s.Mux.HandleFunc("GET /v1/usage", s.requireAuth(s.handleUsageSummary))
}

func (s *Server) usageStore() (store.UsageStore, bool) {
	us, ok := s.Store.(store.UsageStore)
	return us, ok
}

// usageBody is the optional usage object on turns and usage records.
type usageBody struct {
	Key        string     `json:"key"`
	Idx        *int64     `json:"idx"`
	Model      string     `json:"model"`
	Input      int64      `json:"input"`
	Output     int64      `json:"output"`
	CacheRead  int64      `json:"cache_read"`
	CacheWrite int64      `json:"cache_write"`
	CostUSD    *float64   `json:"cost_usd"`
	SourceAt   *time.Time `json:"source_ts"`
}

// record turns a body into a store record. turnIdx/at fill in for usage
// sent on a turn; a record without key or idx cannot be deduplicated.
func (b usageBody) record(sessionID string, turnIdx *int64, at *time.Time) (store.SessionUsage, error) {
	if b.Idx == nil {
		b.Idx = turnIdx
	}
	key := strings.TrimSpace(b.Key)
	if key == "" {
		if b.Idx == nil {
			return store.SessionUsage{}, errors.New("usage needs a key or an idx")
		}
		key = store.UsageKeyForTurn(*b.Idx)
	}
	src := validAgentSourceTime(b.SourceAt)
	if src == nil {
		src = validAgentSourceTime(at)
	}
	rec := store.SessionUsage{SessionID: sessionID, Key: key, TurnIdx: b.Idx, SourceAt: src,
		Entry: usage.Entry{Model: strings.TrimSpace(b.Model), Input: b.Input, Output: b.Output,
			CacheRead: b.CacheRead, CacheWrite: b.CacheWrite, CostUSD: b.CostUSD}}
	if !rec.Entry.Valid() {
		return rec, errors.New("usage values out of range")
	}
	return rec, nil
}

// recordTurnUsage stores usage sent on a turn. Best effort: usage never
// fails the turn upload it rode on.
func (s *Server) recordTurnUsage(r *http.Request, sessionID string, idx int, at *time.Time, body *usageBody) {
	if body == nil {
		return
	}
	us, ok := s.usageStore()
	if !ok {
		return
	}
	i := int64(idx)
	rec, err := body.record(sessionID, &i, at)
	if err != nil || rec.Entry.Empty() {
		return
	}
	_ = us.RecordSessionUsage(r.Context(), []store.SessionUsage{rec})
}

func (s *Server) handleSessionUsagePost(w http.ResponseWriter, r *http.Request) {
	us, ok := s.usageStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "usage unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.rejectIfCaptureOff(w, r, row.ProjectID) {
		return
	}
	var body struct {
		Records []usageBody `json:"records"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Records) == 0 || len(body.Records) > 500 {
		writeError(w, http.StatusBadRequest, "records must contain 1 to 500 items")
		return
	}
	records := make([]store.SessionUsage, 0, len(body.Records))
	for _, b := range body.Records {
		rec, err := b.record(row.ID, nil, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !rec.Entry.Empty() {
			records = append(records, rec)
		}
	}
	if len(records) > 0 {
		if err := us.RecordSessionUsage(r.Context(), records); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recorded": len(records)})
}

func usageTotal(items []usage.Total) usage.Total {
	var t usage.Total
	for _, item := range items {
		t.Merge(item)
	}
	return t
}

func (s *Server) handleSessionUsageGet(w http.ResponseWriter, r *http.Request) {
	us, ok := s.usageStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "usage unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	items, err := us.SessionUsageByModel(r.Context(), row.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []usage.Total{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": row.ID, "harness": row.Harness, "by_model": items, "total": usageTotal(items)})
}

// parseUsageTime accepts RFC 3339 or a YYYY-MM-DD date (midnight in zone).
func parseUsageTime(v string, zone *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, zone)
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	us, ok := s.usageStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "usage unavailable")
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("utc_offset_minutes"))
	if offset < -840 || offset > 840 {
		offset = 0
	}
	zone := time.FixedZone("usage", offset*60)
	from, err := parseUsageTime(q.Get("from"), zone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "from must be RFC 3339 or YYYY-MM-DD")
		return
	}
	to, err := parseUsageTime(q.Get("to"), zone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "to must be RFC 3339 or YYYY-MM-DD")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(q.Get("scope")))
	if scope != "" && scope != "readable" && scope != "mine" {
		writeError(w, http.StatusBadRequest, "scope must be readable or mine")
		return
	}
	group := q.Get("group_by")
	if group == "" {
		group = "agent"
	}
	items, err := us.UsageSummary(r.Context(), store.UsageQuery{
		UserID: authSubject(r), From: from, To: to, ProjectID: strings.TrimSpace(q.Get("project_id")),
		GroupBy: group, OwnerOnly: scope == "mine", TZOffset: offset,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if items == nil {
		items = []usage.Total{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"group_by": strings.ToLower(group), "items": items, "total": usageTotal(items)})
}
