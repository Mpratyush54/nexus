package core

// sessions.usage {session_id}
//   Totals for one chat by model: the cloud ledger
//   (GET /v1/agent-sessions/{id}/usage) plus live turns saved locally that
//   have not reached the cloud. Offline or on a server without usage,
//   local turns alone.
// usage.summary {from, to, project_id, group_by, scope, utc_offset_minutes}
//   GET /v1/usage for the signed-in user (cloudRequest in nxcall.go).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"central-memory/internal/usage"
)

type sessionUsageResult struct {
	SessionID  string        `json:"session_id"`
	ByModel    []usage.Total `json:"by_model"`
	Total      usage.Total   `json:"total"`
	Source     string        `json:"source"` // cloud | local | cloud+local
	CloudError string        `json:"cloud_error,omitempty"`
}

func sessionUsage(ctx context.Context, deps Deps, args json.RawMessage) (any, error) {
	id := argString(args, "session_id", "id")
	if id == "" {
		return nil, errors.New("session id is required")
	}
	out := sessionUsageResult{SessionID: id, ByModel: []usage.Total{}}
	byModel := map[string]*usage.Total{}
	var order []string
	add := func(t usage.Total) {
		key := t.Model
		if byModel[key] == nil {
			byModel[key] = &usage.Total{Key: key, Model: key}
			order = append(order, key)
		}
		byModel[key].Merge(t)
	}

	cloudOK := false
	if deps.Cloud != nil && deps.Online {
		raw, err := deps.Cloud.Do(ctx, http.MethodGet, "/v1/agent-sessions/"+url.PathEscape(id)+"/usage", nil)
		var resp struct {
			ByModel []usage.Total `json:"by_model"`
		}
		if err == nil && json.Unmarshal(raw, &resp) == nil {
			cloudOK = true
			for _, t := range resp.ByModel {
				add(t)
			}
		} else if err != nil {
			out.CloudError = err.Error()
		}
	}

	local := false
	if items, err := loadContinuations(deps.ContinuationDir, id); err == nil {
		for _, ex := range items {
			// A cloud-saved exchange already sent its usage to the ledger.
			if cloudOK && ex.CloudSaved {
				continue
			}
			for _, e := range ex.Usage {
				var t usage.Total
				t.Model = e.Model
				t.Add(e)
				add(t)
				local = true
			}
		}
	}
	switch {
	case cloudOK && local:
		out.Source = "cloud+local"
	case cloudOK:
		out.Source = "cloud"
	default:
		out.Source = "local"
	}
	for _, k := range order {
		out.ByModel = append(out.ByModel, *byModel[k])
		out.Total.Merge(*byModel[k])
	}
	return out, nil
}

// usageSummaryPath builds GET /v1/usage from the call arguments.
func usageSummaryPath(args json.RawMessage) string {
	q := queryArgs(args, map[string]string{
		"from": "from", "to": "to", "project_id": "project_id", "group_by": "group_by",
		"scope": "scope", "utc_offset_minutes": "utc_offset_minutes",
	})
	return "/v1/usage" + strings.TrimSpace(q)
}
