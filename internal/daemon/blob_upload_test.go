package daemon

// Transcript blob uploads against a fake server with the real server's
// limits (internal/server): JSON bodies are capped at 2 MB (decodeJSON,
// maxRequestBodyBytes) except the chunk routes at 32 MB
// (maxChunkRequestBytes, 8 MiB), pieces are hash-checked, and assembly checks the
// assembled file's sha256.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"central-memory/internal/store"
)

const (
	serverJSONCap  = 2 << 20 // internal/server maxRequestBodyBytes
	serverChunkCap = 8 << 20 // internal/server maxChunkRequestBytes
)

type blobPiece struct {
	total int
	sha   string
	body  []byte
}

type strictBlobServer struct {
	mu     sync.Mutex
	blobs  map[string][]byte
	pieces map[string]map[int]blobPiece
	calls  map[string]int
	maxReq map[string]int64
	// failChunk makes the n-th attempt at a piece fail: index -> statuses.
	failChunk map[int][]int
	// dropBeforeAssemble loses the stored pieces once (server restart).
	dropBeforeAssemble bool
	// oldAssemble behaves like a server without assemble's purpose field.
	oldAssemble bool
	purposes    []string
}

func newStrictBlobServer() *strictBlobServer {
	return &strictBlobServer{blobs: map[string][]byte{}, pieces: map[string]map[int]blobPiece{},
		calls: map[string]int{}, maxReq: map[string]int64{}, failChunk: map[int][]int{}}
}

func (s *strictBlobServer) decode(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *strictBlobServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	route := r.Method + " " + path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[1] == "blobs" && len(parts[2]) == 64 {
		route = r.Method + " /v1/blobs/{sha}"
		if len(parts) == 4 {
			route += "/" + parts[3]
		}
	}
	s.mu.Lock()
	s.calls[route]++
	if r.ContentLength > s.maxReq[route] {
		s.maxReq[route] = r.ContentLength
	}
	s.mu.Unlock()
	switch {
	case path == "/v1/agent-sessions":
		var body map[string]any
		if !s.decode(w, r, serverJSONCap, &body) {
			return
		}
		fmt.Fprint(w, `{"id":"cloud-1"}`)
	case path == "/v1/blobs/missing":
		var body struct {
			ProjectID string   `json:"project_id"`
			Hashes    []string `json:"hashes"`
		}
		if !s.decode(w, r, serverJSONCap, &body) {
			return
		}
		missing := []string{}
		s.mu.Lock()
		for _, h := range body.Hashes {
			if _, ok := s.blobs[h]; !ok {
				missing = append(missing, h)
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"missing": missing})
	case route == "PUT /v1/blobs/{sha}":
		var body struct {
			ProjectID string `json:"project_id"`
			BodyB64   string `json:"body_b64"`
			Kind      string `json:"kind"`
			Purpose   string `json:"purpose"`
		}
		if !s.decode(w, r, serverJSONCap, &body) {
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(body.BodyB64)
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != parts[2] {
			http.Error(w, "sha256 does not match body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.blobs[parts[2]] = raw
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case route == "POST /v1/blobs/{sha}/chunks":
		var body struct {
			ProjectID   string `json:"project_id"`
			Index       int    `json:"index"`
			Total       int    `json:"total"`
			BodyB64     string `json:"body_b64"`
			ChunkSHA256 string `json:"chunk_sha256"`
		}
		if !s.decode(w, r, serverChunkCap, &body) {
			return
		}
		s.mu.Lock()
		if fails := s.failChunk[body.Index]; len(fails) > 0 {
			s.failChunk[body.Index] = fails[1:]
			s.mu.Unlock()
			http.Error(w, "injected", fails[0])
			return
		}
		s.mu.Unlock()
		if body.Total < 1 || body.Index < 0 || body.Index >= body.Total {
			http.Error(w, "chunk index out of range", http.StatusBadRequest)
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(body.BodyB64)
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != body.ChunkSHA256 {
			http.Error(w, "chunk_sha256 does not match body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		set := s.pieces[parts[2]]
		if set == nil {
			set = map[int]blobPiece{}
			s.pieces[parts[2]] = set
		}
		for _, p := range set {
			if p.total != body.Total {
				s.mu.Unlock()
				http.Error(w, "chunk total mismatch", http.StatusBadRequest)
				return
			}
		}
		set[body.Index] = blobPiece{total: body.Total, sha: body.ChunkSHA256, body: raw}
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case route == "POST /v1/blobs/{sha}/assemble":
		var body struct {
			ProjectID string `json:"project_id"`
			Purpose   string `json:"purpose"`
		}
		var oldBody struct {
			ProjectID string `json:"project_id"`
		}
		if s.oldAssemble {
			// Servers before the purpose field reject unknown fields.
			if !s.decode(w, r, serverChunkCap, &oldBody) {
				return
			}
		} else if !s.decode(w, r, serverChunkCap, &body) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.purposes = append(s.purposes, body.Purpose)
		if s.dropBeforeAssemble {
			s.dropBeforeAssemble = false
			delete(s.pieces, parts[2])
		}
		set := s.pieces[parts[2]]
		if len(set) == 0 {
			http.Error(w, "missing chunk", http.StatusConflict)
			return
		}
		var total int
		for _, p := range set {
			total = p.total
		}
		var all []byte
		for i := 0; i < total; i++ {
			p, ok := set[i]
			if !ok {
				http.Error(w, "missing chunk", http.StatusConflict)
				return
			}
			all = append(all, p.body...)
		}
		sum := sha256.Sum256(all)
		if hex.EncodeToString(sum[:]) != parts[2] {
			http.Error(w, "could not store assembled file: store: blob bytes do not match sha256", http.StatusBadRequest)
			return
		}
		s.blobs[parts[2]] = all
		fmt.Fprintf(w, `{"sha256":%q,"size":%d,"chunk_count":%d}`, parts[2], len(all), total)
	case strings.HasSuffix(path, "/versions"):
		var body map[string]any
		if !s.decode(w, r, serverJSONCap, &body) {
			return
		}
		fmt.Fprint(w, `{"version":1}`)
	case strings.HasSuffix(path, "/complete"):
		var body map[string]any
		if !s.decode(w, r, serverJSONCap, &body) {
			return
		}
		fmt.Fprint(w, `{"state":"complete"}`)
	default:
		http.Error(w, "unexpected "+route, http.StatusNotFound)
	}
}

func (s *strictBlobServer) count(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[route]
}

// transcriptSnapshot gzips a JSONL transcript of about n bytes.
func transcriptSnapshot(t *testing.T, n int) (*store.SessionSnapshot, []byte) {
	t.Helper()
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		b.WriteString(claudeUserLine(i, fmt.Sprintf("%d %s", i, filler(2000+i%977))))
		b.WriteByte('\n')
	}
	gz, lines, err := gzipSnapshotTranscript(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return &store.SessionSnapshot{Harness: "claude", ConversationID: "native-1", SourceMachineID: "m1",
		TranscriptPayload: gz, TurnCount: lines}, b.Bytes()
}

func publishTo(t *testing.T, srv *httptest.Server, snap *store.SessionSnapshot) error {
	t.Helper()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "11111111-2222-4333-8444-555555555555")
	return hs.PublishAgentSessionVersion(context.Background(), snap, "C:/w")
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestSmallTranscriptUsesPlainPut(t *testing.T) {
	fake := newStrictBlobServer()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	snap, raw := transcriptSnapshot(t, 200<<10)
	if err := publishTo(t, srv, snap); err != nil {
		t.Fatal(err)
	}
	if fake.count("PUT /v1/blobs/{sha}") != 1 || fake.count("POST /v1/blobs/{sha}/chunks") != 0 {
		t.Fatalf("calls = %v", fake.calls)
	}
	if !bytes.Equal(fake.blobs[sha(raw)], raw) {
		t.Fatal("stored blob differs from the transcript")
	}
}

// A large transcript is rejected by the plain PUT's 2 MB cap, so it goes in
// pieces, each request under the 32 MB chunk cap, and assembles verified.
func TestLargeTranscriptUploadsInChunks(t *testing.T) {
	fake := newStrictBlobServer()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	snap, raw := transcriptSnapshot(t, 18<<20)
	if err := publishTo(t, srv, snap); err != nil {
		t.Fatal(err)
	}
	want := int((int64(len(raw)) + blobChunkBytes - 1) / blobChunkBytes)
	if n := fake.count("POST /v1/blobs/{sha}/chunks"); n != want {
		t.Fatalf("pieces sent = %d, want %d", n, want)
	}
	if fake.count("PUT /v1/blobs/{sha}") != 0 || fake.count("POST /v1/blobs/{sha}/assemble") != 1 {
		t.Fatalf("calls = %v", fake.calls)
	}
	if !bytes.Equal(fake.blobs[sha(raw)], raw) {
		t.Fatal("assembled blob differs from the transcript")
	}
	if max := fake.maxReq["POST /v1/blobs/{sha}/chunks"]; max > serverChunkCap {
		t.Fatalf("largest piece request %d bytes, over the server cap", max)
	}
	// Already on the server: nothing is uploaded again.
	before := fake.count("POST /v1/blobs/{sha}/chunks")
	if err := publishTo(t, srv, snap); err != nil {
		t.Fatal(err)
	}
	if fake.count("POST /v1/blobs/{sha}/chunks") != before || fake.count("PUT /v1/blobs/{sha}") != 0 {
		t.Fatalf("existing blob uploaded again: %v", fake.calls)
	}
}

// The client never builds the whole upload: it holds one piece.
func TestChunkedUploadMemoryIsOnePiece(t *testing.T) {
	snap, raw := transcriptSnapshot(t, 24<<20)
	cloud := &countingCloud{calls: map[string]int{}, bytes: map[string]int64{}}
	srv := httptest.NewServer(cloud) // drains bodies without decoding
	defer srv.Close()
	var err error
	alloc := allocDuring(func() { err = publishTo(t, srv, snap) })
	if err != nil {
		t.Fatal(err)
	}
	if n := cloud.count("POST /v1/blobs/chunks"); n != int((int64(len(raw))+blobChunkBytes-1)/blobChunkBytes) {
		t.Fatalf("pieces = %d (%v)", n, cloud.calls)
	}
	if limit := uint64(blobChunkBytes) + 6<<20; !raceEnabled && alloc > limit {
		t.Fatalf("chunked upload of %d MB allocated %.1f MB, want < %.1f MB", len(raw)>>20, float64(alloc)/(1<<20), float64(limit)/(1<<20))
	}
}

func TestChunkedUploadRetriesAndResumes(t *testing.T) {
	old := blobChunkBackoff
	blobChunkBackoff = 0
	defer func() { blobChunkBackoff = old }()

	fake := newStrictBlobServer()
	fake.failChunk[1] = []int{http.StatusServiceUnavailable} // transient: retried in place
	fake.failChunk[2] = []int{http.StatusBadRequest}         // permanent for this publish
	srv := httptest.NewServer(fake)
	defer srv.Close()
	snap, raw := transcriptSnapshot(t, 18<<20)
	hs := NewHTTPMemoryStore(srv.URL, "tok", "11111111-2222-4333-8444-555555555555")
	if err := hs.PublishAgentSessionVersion(context.Background(), snap, "C:/w"); err == nil {
		t.Fatal("publish should fail on the rejected piece")
	}
	if got := fake.count("POST /v1/blobs/{sha}/chunks"); got != 4 { // 0, 1 (twice), 2
		t.Fatalf("first publish sent %d piece requests, want 4", got)
	}
	// The next publish resumes at piece 2.
	if err := hs.PublishAgentSessionVersion(context.Background(), snap, "C:/w"); err != nil {
		t.Fatal(err)
	}
	total := int((int64(len(raw)) + blobChunkBytes - 1) / blobChunkBytes)
	if got := fake.count("POST /v1/blobs/{sha}/chunks"); got != 4+total-2 {
		t.Fatalf("resumed publish: %d piece requests in all, want %d", got, 4+total-2)
	}
	if !bytes.Equal(fake.blobs[sha(raw)], raw) {
		t.Fatal("assembled blob differs")
	}
}

func TestChunkedUploadStartsOverWhenServerLostPieces(t *testing.T) {
	fake := newStrictBlobServer()
	fake.dropBeforeAssemble = true
	srv := httptest.NewServer(fake)
	defer srv.Close()
	snap, raw := transcriptSnapshot(t, 9<<20)
	if err := publishTo(t, srv, snap); err != nil {
		t.Fatal(err)
	}
	total := int((int64(len(raw)) + blobChunkBytes - 1) / blobChunkBytes)
	if got := fake.count("POST /v1/blobs/{sha}/chunks"); got != 2*total {
		t.Fatalf("piece requests = %d, want %d (everything twice)", got, 2*total)
	}
	if fake.count("POST /v1/blobs/{sha}/assemble") != 2 || !bytes.Equal(fake.blobs[sha(raw)], raw) {
		t.Fatalf("calls = %v", fake.calls)
	}
}

// The fake enforces the cap the real server has: a large plain PUT fails.
func TestStrictServerRejectsLargePlainPut(t *testing.T) {
	fake := newStrictBlobServer()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	old := blobPutMaxBytes
	blobPutMaxBytes = 1 << 40 // force the plain route
	defer func() { blobPutMaxBytes = old }()
	snap, _ := transcriptSnapshot(t, 3<<20)
	if err := publishTo(t, srv, snap); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("plain PUT of 3 MB: err = %v, want a 400 from the 2 MB cap", err)
	}
}

func TestAssembleDeclaresTranscriptPurpose(t *testing.T) {
	for _, old := range []bool{false, true} {
		fake := newStrictBlobServer()
		fake.oldAssemble = old
		srv := httptest.NewServer(fake)
		snap, raw := transcriptSnapshot(t, 6<<20)
		err := publishTo(t, srv, snap)
		srv.Close()
		if err != nil {
			t.Fatalf("old=%v: %v", old, err)
		}
		if !bytes.Equal(fake.blobs[sha(raw)], raw) {
			t.Fatalf("old=%v: blob not assembled", old)
		}
		if !old && (len(fake.purposes) != 1 || fake.purposes[0] != "transcript") {
			t.Fatalf("assemble purposes = %q, want [transcript]", fake.purposes)
		}
		if old && fake.count("POST /v1/blobs/{sha}/assemble") != 2 {
			t.Fatalf("old server: %d assemble requests, want 2 (retry without purpose)", fake.count("POST /v1/blobs/{sha}/assemble"))
		}
	}
}
