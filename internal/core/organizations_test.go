package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOrganizationMethodsUseContractRoutes(t *testing.T) {
	cases := []struct{ method, args, verb, path string }{
		{"orgs.list", `{}`, "GET", "/orgs"},
		{"orgs.create", `{"name":"Team"}`, "POST", "/orgs"},
		{"orgs.get", `{"org_id":"o1"}`, "GET", "/orgs/o1"},
		{"orgs.member_role", `{"org_id":"o1","user_id":"u1","role":"ADMIN"}`, "PUT", "/orgs/o1/members/u1/role"},
		{"orgs.member_remove", `{"org_id":"o1","user_id":"u1"}`, "DELETE", "/orgs/o1/members/u1"},
		{"orgs.invites", `{"org_id":"o1"}`, "GET", "/orgs/o1/invites"},
		{"orgs.invite_create", `{"org_id":"o1","email":"a@example.com"}`, "POST", "/orgs/o1/invites"},
		{"orgs.invite_revoke", `{"org_id":"o1","invite_id":"i1"}`, "DELETE", "/orgs/o1/invites/i1"},
		{"orgs.invite_accept", `{"token":"code"}`, "POST", "/orgs/invites/accept"},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			cloud := &routeCloud{result: []byte(`{}`)}
			if _, err := Call(context.Background(), c.method, json.RawMessage(c.args), Deps{Cloud: cloud, Online: true}); err != nil {
				t.Fatal(err)
			}
			if cloud.method != c.verb || cloud.path != c.path {
				t.Fatalf("got %s %s, want %s %s", cloud.method, cloud.path, c.verb, c.path)
			}
		})
	}
}

func TestOrganizationMethodsRejectMissingFields(t *testing.T) {
	for _, method := range []string{"orgs.create", "orgs.get", "orgs.member_role", "orgs.member_remove", "orgs.invite_create", "orgs.invites", "orgs.invite_revoke", "orgs.invite_accept"} {
		if _, _, _, err := cloudRequest(method, json.RawMessage(`{}`)); err == nil {
			t.Errorf("%s accepts missing arguments", method)
		}
	}
}
