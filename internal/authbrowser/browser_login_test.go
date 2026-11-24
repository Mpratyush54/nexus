package authbrowser

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginRejectsCallbackWithoutState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authURL := make(chan *url.URL, 1)
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := Login(ctx, Options{
			AppURL: "https://nexus.example",
			OpenURL: func(raw string) error {
				u, err := url.Parse(raw)
				if err != nil {
					return err
				}
				authURL <- u
				return nil
			},
		})
		done <- outcome{err: err}
	}()

	var opened *url.URL
	select {
	case opened = <-authURL:
	case <-ctx.Done():
		t.Fatal("browser URL was not opened")
	}
	redirect := opened.Query().Get("redirect")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Fatalf("callback redirect = %q", redirect)
	}
	resp, err := http.Get(redirect)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing state status = %d, want 400", resp.StatusCode)
	}
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("login accepted a callback without state")
		}
	case <-ctx.Done():
		t.Fatal("login did not reject the callback")
	}
}
