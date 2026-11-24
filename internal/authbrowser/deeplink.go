package authbrowser

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"central-memory/internal/config"
)

const pendingLoginTTL = 5 * time.Minute

// PendingLogin is one in-flight browser sign-in delivered through the app
// scheme. It carries the URL to open plus the context needed after the
// callback returns.
type PendingLogin struct {
	State     string
	AuthURL   string
	Redirect  string
	AppURL    string
	ServerURL string
	ExpiresAt time.Time
}

type pendingLogin struct {
	PendingLogin
}

var pendingLogins = struct {
	sync.Mutex
	byState map[string]pendingLogin
}{byState: map[string]pendingLogin{}}

// BeginLogin registers a one-use state and builds the web sign-in URL for the
// installed desktop app. No loopback listener is opened.
func BeginLogin(appURL, serverURL string, now time.Time) (PendingLogin, error) {
	appURL = strings.TrimRight(strings.TrimSpace(appURL), "/")
	if appURL == "" {
		appURL = config.ResolveAppURL()
	}
	if appURL == "" {
		return PendingLogin{}, errors.New("authbrowser: web app URL is unavailable")
	}
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = config.ResolveServerURL("")
	}
	if now.IsZero() {
		now = time.Now()
	}
	state, err := randomState()
	if err != nil {
		return PendingLogin{}, err
	}
	expires := now.UTC().Add(pendingLoginTTL)
	authURL := appURL + "/cli/auth?" + url.Values{
		"redirect": {AppCallback},
		"state":    {state},
	}.Encode()
	pending := PendingLogin{
		State:     state,
		AuthURL:   authURL,
		Redirect:  AppCallback,
		AppURL:    appURL,
		ServerURL: serverURL,
		ExpiresAt: expires,
	}
	pendingLogins.Lock()
	prunePendingLocked(now)
	pendingLogins.byState[state] = pendingLogin{PendingLogin: pending}
	pendingLogins.Unlock()
	return pending, nil
}

// CompleteLogin validates a nexus://auth/callback URL and consumes its state.
func CompleteLogin(rawURL string, now time.Time) (Result, PendingLogin, error) {
	if now.IsZero() {
		now = time.Now()
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "nexus") ||
		!strings.EqualFold(parsed.Host, "auth") || callbackPath(parsed.Path) != "/callback" {
		return Result{}, PendingLogin{}, errors.New("authbrowser: invalid callback URL")
	}
	values := callbackValues(parsed)
	state := strings.TrimSpace(values.Get("state"))
	token := strings.TrimSpace(values.Get("token"))
	userID := strings.TrimSpace(values.Get("user_id"))
	username := strings.TrimSpace(values.Get("username"))
	if state == "" {
		return Result{}, PendingLogin{}, errors.New("authbrowser: callback missing state")
	}

	pendingLogins.Lock()
	defer pendingLogins.Unlock()
	prunePendingLocked(now)
	pending, ok := pendingLogins.byState[state]
	if !ok {
		return Result{}, PendingLogin{}, errors.New("authbrowser: unknown or expired login state")
	}
	if !now.UTC().Before(pending.ExpiresAt) {
		delete(pendingLogins.byState, state)
		return Result{}, PendingLogin{}, errors.New("authbrowser: login state expired")
	}
	if token == "" || userID == "" {
		return Result{}, PendingLogin{}, errors.New("authbrowser: callback missing token or user_id")
	}
	delete(pendingLogins.byState, state)
	return Result{Token: token, UserID: userID, Username: username}, pending.PendingLogin, nil
}

func callbackPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}

func callbackValues(parsed *url.URL) url.Values {
	values := url.Values{}
	if fragment, err := url.ParseQuery(parsed.Fragment); err == nil {
		for key, list := range fragment {
			values[key] = append(values[key], list...)
		}
	}
	for key, list := range parsed.Query() {
		if len(values[key]) == 0 {
			values[key] = append(values[key], list...)
		}
	}
	return values
}

func prunePendingLocked(now time.Time) {
	for state, pending := range pendingLogins.byState {
		if !now.UTC().Before(pending.ExpiresAt) {
			delete(pendingLogins.byState, state)
		}
	}
}
