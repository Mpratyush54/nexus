package authbrowser

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBeginLoginUsesAppCallbackAndState(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	pending, err := BeginLogin("https://nexus.example/", "https://api.example", now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Redirect != AppCallback || pending.AppURL != "https://nexus.example" {
		t.Fatalf("pending = %+v", pending)
	}
	parsed, err := url.Parse(pending.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/cli/auth" || parsed.Query().Get("redirect") != AppCallback ||
		parsed.Query().Get("state") != pending.State {
		t.Fatalf("auth URL = %s", pending.AuthURL)
	}
	if !pending.ExpiresAt.Equal(now.Add(pendingLoginTTL)) {
		t.Fatalf("expiry = %s", pending.ExpiresAt)
	}
}

func TestCompleteLoginReadsFragmentOnce(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	pending, err := BeginLogin("https://nexus.example", "https://api.example", now)
	if err != nil {
		t.Fatal(err)
	}
	raw := AppCallback + "#token=desktop-token&user_id=user-1&username=ada&state=" +
		url.QueryEscape(pending.State)
	result, completed, err := CompleteLogin(raw, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "desktop-token" || result.UserID != "user-1" || result.Username != "ada" {
		t.Fatalf("result = %+v", result)
	}
	if completed.ServerURL != "https://api.example" || completed.AppURL != "https://nexus.example" {
		t.Fatalf("completed = %+v", completed)
	}
	if _, _, err := CompleteLogin(raw, now.Add(time.Minute)); err == nil {
		t.Fatal("second callback should fail after state is consumed")
	}
}

func TestCompleteLoginAcceptsQueryFallback(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	pending, err := BeginLogin("https://nexus.example", "", now)
	if err != nil {
		t.Fatal(err)
	}
	raw := AppCallback + "?token=desktop-token&user_id=user-1&state=" + url.QueryEscape(pending.State)
	if _, _, err := CompleteLogin(raw, now); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteLoginRejectsWrongStateAndScheme(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	pending, err := BeginLogin("https://nexus.example", "", now)
	if err != nil {
		t.Fatal(err)
	}
	raw := AppCallback + "#token=t&user_id=u&state=wrong"
	if _, _, err := CompleteLogin(raw, now); err == nil {
		t.Fatal("wrong state should be rejected")
	}
	raw = "https://evil.example/auth/callback#token=t&user_id=u&state=" +
		url.QueryEscape(pending.State)
	if _, _, err := CompleteLogin(raw, now); err == nil {
		t.Fatal("non-nexus scheme should be rejected")
	}
	raw = "nexus://other/callback#token=t&user_id=u&state=" + url.QueryEscape(pending.State)
	if _, _, err := CompleteLogin(raw, now); err == nil {
		t.Fatal("wrong host should be rejected")
	}
	if _, _, err := CompleteLogin(AppCallback+"#token=t&user_id=u&state="+url.QueryEscape(pending.State), now); err != nil {
		t.Fatalf("pending state should survive rejected callbacks: %v", err)
	}
}

func TestCompleteLoginExpires(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	pending, err := BeginLogin("https://nexus.example", "", now)
	if err != nil {
		t.Fatal(err)
	}
	raw := AppCallback + "#token=t&user_id=u&state=" + url.QueryEscape(pending.State)
	_, _, err = CompleteLogin(raw, now.Add(pendingLoginTTL+time.Second))
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}
