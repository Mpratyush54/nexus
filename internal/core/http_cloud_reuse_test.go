package core

import (
	"compress/gzip"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudClientIsSharedPerTimeout(t *testing.T) {
	a := cloudClient(cloudDefaultTimeout)
	b := cloudClient(cloudDefaultTimeout)
	if a != b {
		t.Fatal("same timeout must reuse one client")
	}
	long := cloudClient(cloudBlobTimeout)
	if long == a || long.Timeout != cloudBlobTimeout || a.Timeout != cloudDefaultTimeout {
		t.Fatalf("timeouts: default=%v blob=%v", a.Timeout, long.Timeout)
	}
	if a.Transport != cloudTransport || long.Transport != cloudTransport {
		t.Fatal("every cloud client must share cloudTransport")
	}
	if cloudTransport.MaxIdleConnsPerHost < 8 || cloudTransport.DisableKeepAlives || cloudTransport.DisableCompression {
		t.Fatalf("transport: idle/host=%d keepalive-off=%v compression-off=%v",
			cloudTransport.MaxIdleConnsPerHost, cloudTransport.DisableKeepAlives, cloudTransport.DisableCompression)
	}
}

// Opening a chat issues several requests at once. A second burst must reuse
// the connections of the first instead of handshaking again, and responses
// must be accepted gzip-compressed.
func TestHTTPCloudReusesConnectionsAndAcceptsGzip(t *testing.T) {
	const burst = 8
	var newConns atomic.Int32
	var gate atomic.Pointer[sync.WaitGroup]
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g := gate.Load(); g != nil {
			g.Done()
			g.Wait()
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			http.Error(w, "gzip not accepted", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(`{"items":[]}`))
		_ = gz.Close()
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	wave := func() {
		arrived := &sync.WaitGroup{}
		arrived.Add(burst)
		gate.Store(arrived)
		var done sync.WaitGroup
		errs := make(chan error, burst)
		for i := 0; i < burst; i++ {
			done.Add(1)
			go func() {
				defer done.Done()
				// A new HTTPCloud per call mirrors nx_init re-creating it.
				raw, err := (&HTTPCloud{BaseURL: srv.URL, Token: "token"}).Do(context.Background(), http.MethodGet, "/v1/timeline", nil)
				if err == nil && string(raw) != `{"items":[]}` {
					err = &CloudError{Msg: "body not decoded: " + string(raw)}
				}
				errs <- err
			}()
		}
		done.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		gate.Store(nil)
	}
	wave()
	// Idle connections are handed back to the pool just after the body closes.
	time.Sleep(100 * time.Millisecond)
	wave()
	if got := newConns.Load(); got > burst {
		t.Fatalf("opened %d connections for two bursts of %d; keep-alive pool is not reused", got, burst)
	}
}
