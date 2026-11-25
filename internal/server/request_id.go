package server

// request_id.go — every response carries X-Request-ID, every error body
// carries the same ID as error.request_id, and the access log line for the
// request includes it, so a user's "ref ABC123" or a problem report can be
// matched to the server log. Panics in handlers are recovered here, logged
// with their stack and the request ID, and answered with a 500 envelope.

import (
	"context"
	"crypto/rand"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
)

// RequestIDHeader is the response (and accepted request) header name.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// Crockford base32 without I, L, O, U: easy to read back over the phone.
const requestIDAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newRequestID returns 12 characters (60 bits) of Crockford base32.
func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = requestIDAlphabet[int(b[i])%len(requestIDAlphabet)]
	}
	return string(b[:])
}

// An upstream proxy may already have assigned an ID; keep it when it is a
// plain token so it cannot inject anything into log lines.
var inboundRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestIDFrom returns the request ID stored on ctx by withRequestID.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestIDOf reads the ID from a request (context first, then header).
func requestIDOf(r *http.Request) string {
	if id := RequestIDFrom(r.Context()); id != "" {
		return id
	}
	return r.Header.Get(RequestIDHeader)
}

// withRequestID assigns the request ID, exposes it to browsers and
// recovers handler panics.
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(RequestIDHeader))
		if !inboundRequestID.MatchString(id) {
			id = newRequestID()
		}
		r.Header.Set(RequestIDHeader, id)
		h := w.Header()
		h.Set(RequestIDHeader, id)
		h.Add("Access-Control-Expose-Headers", RequestIDHeader)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.Log.Printf("panic rid=%s %s %s: %v\n%s", id, r.Method, r.URL.Path, v, debug.Stack())
			writeError(w, http.StatusInternalServerError, "internal server error")
		}()
		next.ServeHTTP(w, r)
	})
}
