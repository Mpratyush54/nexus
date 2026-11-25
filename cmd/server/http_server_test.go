package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(":0", http.NewServeMux())
	if s.ReadHeaderTimeout != 10*time.Second || s.ReadTimeout != 2*time.Minute || s.IdleTimeout != 2*time.Minute {
		t.Fatalf("timeouts: header=%s read=%s idle=%s", s.ReadHeaderTimeout, s.ReadTimeout, s.IdleTimeout)
	}
}
