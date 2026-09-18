package github

import (
	"strings"
	"testing"
)

func TestOAuthConfigAuthorizeURL(t *testing.T) {
	c := OAuthConfig{
		ClientID:     "cid",
		ClientSecret: "sec",
		RedirectURL:  "http://localhost/cb",
	}
	if !c.Configured() {
		t.Fatal("expected configured")
	}
	u := c.AuthorizeURL("st")
	if !strings.Contains(u, "client_id=cid") || !strings.Contains(u, "state=st") {
		t.Fatalf("url = %s", u)
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/acme/nexus.git": {"acme", "nexus"},
		"git@github.com:Acme/Nexus.git":     {"Acme", "Nexus"},
		"github.com/acme/nexus":             {"acme", "nexus"},
		"acme/nexus":                        {"acme", "nexus"},
		"https://gitlab.com/acme/nexus":     {"", ""},
		"":                                  {"", ""},
		"just-a-folder":                     {"", ""},
	}
	for in, want := range cases {
		o, r := ParseOwnerRepo(in)
		if o != want[0] || r != want[1] {
			t.Errorf("ParseOwnerRepo(%q) = %q/%q, want %q/%q", in, o, r, want[0], want[1])
		}
	}
}

func TestMapPermissionToRole(t *testing.T) {
	cases := map[string]string{
		"admin":    "ADMIN",
		"maintain": "ADMIN",
		"write":    "EDITOR",
		"push":     "EDITOR",
		"triage":   "EDITOR",
		"read":     "VIEWER",
		"":         "VIEWER",
		"unknown":  "VIEWER",
	}
	for in, want := range cases {
		if got := MapPermissionToRole(in); got != want {
			t.Errorf("MapPermissionToRole(%q) = %q, want %q", in, got, want)
		}
	}
}
