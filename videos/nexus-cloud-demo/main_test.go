package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeAPI struct {
	call func(string, string, []byte) ([]byte, error)
}

func (f fakeAPI) Do(_ context.Context, m, p string, b []byte) ([]byte, error) { return f.call(m, p, b) }

func TestPrepareReusesOnlyNamedDemoResources(t *testing.T) {
	writes := 0
	c := fakeAPI{call: func(m, p string, b []byte) ([]byte, error) {
		switch {
		case m == "GET" && p == "/orgs":
			return []byte(`{"items":[{"id":"unrelated","name":"Real organization","slug":"other"},{"id":"demo-org","name":"Nexus Demo Studio · Film","slug":"` + demoSlug + `"}]}`), nil
		case m == "GET" && p == "/orgs/demo-org":
			return []byte(`{"projects":[{"id":"demo-project","folder_name":"` + demoFolder + `","display_name":"Checkout Recovery · DEMO"}]}`), nil
		case m == "GET" && strings.HasPrefix(p, "/memory/search?"):
			return []byte(`{"items":[{"id":"m1","key":"DEMO_CHECKOUT_20261024_CART","status":"CONFIRMED"},{"id":"m2","key":"DEMO_CHECKOUT_20261024_REVIEW","status":"CONFIRMED"},{"id":"m3","key":"DEMO_CHECKOUT_20261024_HANDOFF","status":"CONFIRMED"}]}`), nil
		case m == "POST" && p == "/v1/agent/mcp":
			var v map[string]any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatal(err)
			}
			return []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Synthetic demo decision: preserve the cart until payment is confirmed."}]}}`), nil
		default:
			writes++
			return nil, errors.New("unexpected resource mutation")
		}
	}}
	saves := 0
	r, err := prepare(t.Context(), c, func(receipt) error { saves++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 || r.ProjectID != "demo-project" || len(r.MemoryIDs) != 3 || !r.MCPVerified || saves == 0 {
		t.Fatalf("unexpected receipt: %+v mutations=%d", r, writes)
	}
}

func TestPrepareStopsOnOrganizationLimitWithoutProjectWrites(t *testing.T) {
	calls := []string{}
	c := fakeAPI{call: func(m, p string, _ []byte) ([]byte, error) {
		calls = append(calls, m+" "+p)
		if m == "GET" && p == "/orgs" {
			return []byte(`{"items":[]}`), nil
		}
		if m == "POST" && p == "/orgs" {
			return nil, errors.New("organization limit reached")
		}
		t.Fatalf("unexpected call after failure: %s %s", m, p)
		return nil, nil
	}}
	_, err := prepare(t.Context(), c, func(receipt) error { t.Fatal("should not save uncreated organization"); return nil })
	if err == nil || len(calls) != 2 {
		t.Fatalf("failure not preserved: %v %v", err, calls)
	}
}

func TestExistingOrganizationCreatesOnlyDemoProjectAndStopsOnProjectLimit(t *testing.T) {
	calls := []string{}
	c := fakeAPI{call: func(m, p string, b []byte) ([]byte, error) {
		calls = append(calls, m+" "+p)
		switch m + " " + p {
		case "GET /orgs":
			return []byte(`{"items":[{"id":"existing","name":"Existing organization","slug":"real"}]}`), nil
		case "GET /orgs/existing":
			return []byte(`{"projects":[{"id":"private","folder_name":"other-project"}]}`), nil
		case "POST /orgs/existing/projects":
			var body map[string]string
			if err := json.Unmarshal(b, &body); err != nil || body["folder_name"] != demoFolder || len(body) != 2 {
				t.Fatalf("unexpected project input: %s", b)
			}
			return nil, errors.New("project limit reached")
		default:
			t.Fatalf("unexpected mutation or call: %s %s", m, p)
			return nil, nil
		}
	}}
	r, err := prepareWithOrganization(t.Context(), c, func(r receipt) error {
		if r.OrganizationID != "existing" || r.ProjectID != "" {
			t.Fatal("unexpected receipt")
		}
		return nil
	}, true)
	if err == nil || len(calls) != 3 || r.ProjectID != "" {
		t.Fatalf("failure not preserved: %v %v", err, calls)
	}
}

func TestExistingOrganizationRejectsAmbiguousOrMissingOrganizations(t *testing.T) {
	for _, orgs := range []string{`[]`, `[{"id":"one"},{"id":"two"}]`, `[{"name":"missing ID"}]`} {
		t.Run(orgs, func(t *testing.T) {
			c := fakeAPI{call: func(m, p string, _ []byte) ([]byte, error) {
				if m != "GET" || p != "/orgs" {
					t.Fatalf("unexpected call: %s %s", m, p)
				}
				return []byte(`{"items":` + orgs + `}`), nil
			}}
			_, err := prepareWithOrganization(t.Context(), c, func(receipt) error { t.Fatal("must not save"); return nil }, true)
			if err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}
