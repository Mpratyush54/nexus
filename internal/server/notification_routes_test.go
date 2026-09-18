package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/webpush"
)

func TestNotificationListRequiresProject(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/notifications", alice, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationListAndVapid(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "notif-proj")

	rec := doJSON(t, s, http.MethodGet, "/notifications?project_id="+proj, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/notifications/vapid-public-key", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vapid = %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if _, ok := out["configured"]; !ok {
		t.Fatalf("expected configured flag: %+v", out)
	}

	rec = doJSON(t, s, http.MethodPost, "/notifications/subscribe", alice, map[string]any{
		"endpoint": "https://push.example/sub/1",
		"keys":     map[string]string{"p256dh": "abc", "auth": "def"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPushTestSendsVAPID(t *testing.T) {
	keys, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAPID_PUBLIC_KEY", keys.Public)
	t.Setenv("VAPID_PRIVATE_KEY", keys.Private)

	var gotEnc, gotAuth string
	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEnc = r.Header.Get("Content-Encoding")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSrv.Close()

	s := newTestServer()
	s.PushClient = pushSrv.Client()
	alice := loginAs(t, s, "alice")

	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodPost, "/notifications/subscribe", alice, map[string]any{
		"endpoint": pushSrv.URL + "/sub",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/notifications/test", alice, map[string]any{
		"title": "Ping",
		"body":  "hello from nexus",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("test push = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if out["sent"] != float64(1) {
		t.Fatalf("sent = %+v", out)
	}
	if gotEnc != "aes128gcm" || !strings.HasPrefix(gotAuth, "vapid t=") {
		t.Fatalf("enc=%q auth=%q", gotEnc, gotAuth)
	}
}
