package github

import "testing"

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
