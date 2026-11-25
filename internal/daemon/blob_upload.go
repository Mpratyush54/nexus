package daemon

// Transcript blob upload.
//
// The server decodes PUT /v1/blobs/{sha} with its 2 MB JSON body cap, so a
// transcript over about 1.5 MB could never be published that way. Larger
// transcripts go through the chunk routes the server already has:
// POST /v1/blobs/{sha}/chunks (one piece per request, 32 MB cap) and
// POST /v1/blobs/{sha}/assemble, which checks every piece's hash and the
// assembled file's sha256 before storing it.
//
// Pieces are cut from the gzip payload as it decompresses, one at a time,
// so memory stays at one piece whatever the transcript's size. Each piece
// is retried on its own; a publish that fails part-way resumes at the first
// piece the server has not acknowledged (the server keeps pieces until
// assembly), and starts over only when assembly reports a missing piece.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	// blobPutMaxBytes is the largest transcript sent with a plain PUT: its
	// base64 JSON body stays well under the server's 2 MB request cap.
	blobPutMaxBytes int64 = 1 << 20
	// blobChunkBytes is the piece size: its base64 JSON body stays well
	// under the server's 32 MB chunk request cap, and one piece is all the
	// memory an upload holds.
	blobChunkBytes int64 = 4 << 20
	// blobChunkAttempts and blobChunkBackoff bound per-piece retries.
	blobChunkAttempts = 3
	blobChunkBackoff  = 500 * time.Millisecond
)

// errAssembleMissing: the server no longer has every piece (it restarted,
// or dropped them); the upload must start over.
var errAssembleMissing = errors.New("daemon: blob assembly is missing a piece")

// uploadTranscriptBlob stores gz's decompressed transcript (sha256 hash,
// size bytes) as a plain transcript blob unless the server already has it.
func (s *HTTPMemoryStore) uploadTranscriptBlob(ctx context.Context, client *http.Client, base, token, projectID string, gz []byte, hash string, size int64) error {
	if s.blobPresent(ctx, client, base, token, projectID, hash) {
		return nil
	}
	if size <= blobPutMaxBytes {
		length, open, err := streamingJSONBody(map[string]any{
			"project_id": projectID,
			"kind":       "plain",
			"purpose":    "transcript",
		}, []b64Field{gunzipField("body_b64", gz, size)})
		if err != nil {
			return err
		}
		_, err = mirrorStream(ctx, client, http.MethodPut, base+"/v1/blobs/"+hash, token, length, open, nil)
		return err
	}
	err := s.uploadChunked(ctx, client, base, token, projectID, gz, hash, size)
	if errors.Is(err, errAssembleMissing) {
		s.setChunkProgress(projectID, hash, 0)
		err = s.uploadChunked(ctx, client, base, token, projectID, gz, hash, size)
	}
	return err
}

// blobPresent asks POST /v1/blobs/missing whether the server already holds
// hash. Any failure (an older server without the route) means "upload it".
func (s *HTTPMemoryStore) blobPresent(ctx context.Context, client *http.Client, base, token, projectID, hash string) bool {
	var out struct {
		Missing []string `json:"missing"`
	}
	if _, err := mirrorJSON(ctx, client, http.MethodPost, base+"/v1/blobs/missing", token,
		map[string]any{"project_id": projectID, "hashes": []string{hash}}, &out); err != nil || out.Missing == nil {
		return false
	}
	for _, m := range out.Missing {
		if m == hash || m == "sha256:"+hash {
			return false
		}
	}
	return true
}

func (s *HTTPMemoryStore) uploadChunked(ctx context.Context, client *http.Client, base, token, projectID string, gz []byte, hash string, size int64) error {
	total := int((size + blobChunkBytes - 1) / blobChunkBytes)
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer zr.Close()
	start := s.chunkProgress(projectID, hash)
	if start > total {
		start = 0
	}
	// Skip the pieces the server already acknowledged.
	if start > 0 {
		if _, err := io.CopyN(io.Discard, zr, int64(start)*blobChunkBytes); err != nil {
			return err
		}
	}
	endpoint := base + "/v1/blobs/" + hash
	piece := make([]byte, blobChunkBytes)
	for index := start; index < total; index++ {
		n, err := io.ReadFull(zr, piece)
		if err != nil && err != io.ErrUnexpectedEOF && !(err == io.EOF && n > 0) {
			return fmt.Errorf("daemon: blob piece %d/%d: %w", index+1, total, err)
		}
		body := piece[:n]
		sum := sha256.Sum256(body)
		length, open, err := streamingJSONBody(map[string]any{
			"project_id":   projectID,
			"index":        index,
			"total":        total,
			"chunk_sha256": hex.EncodeToString(sum[:]),
		}, []b64Field{bytesField("body_b64", body)})
		if err != nil {
			return err
		}
		if err := retryPiece(ctx, func() error {
			_, err := mirrorStream(ctx, client, http.MethodPost, endpoint+"/chunks", token, length, open, nil)
			return err
		}); err != nil {
			return fmt.Errorf("daemon: blob piece %d/%d: %w", index+1, total, err)
		}
		s.setChunkProgress(projectID, hash, index+1)
	}
	var status int
	err = retryPiece(ctx, func() error {
		var err error
		status, err = mirrorJSON(ctx, client, http.MethodPost, endpoint+"/assemble", token, map[string]any{"project_id": projectID}, nil)
		if status == http.StatusConflict {
			return nil // not transient: handled below
		}
		return err
	})
	if status == http.StatusConflict {
		return errAssembleMissing
	}
	if err != nil {
		return err
	}
	s.setChunkProgress(projectID, hash, -1)
	return nil
}

// retryPiece runs fn up to blobChunkAttempts times, backing off between
// attempts; client errors other than timeouts are not retried.
func retryPiece(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < blobChunkAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(blobChunkBackoff << (attempt - 1)):
			}
		}
		if err = fn(); err == nil {
			return nil
		}
		var se *mirrorStatusError
		if errors.As(err, &se) && se.status >= 400 && se.status < 500 && se.status != http.StatusRequestTimeout && se.status != http.StatusTooManyRequests {
			return err
		}
	}
	return err
}

// chunk upload progress: pieces acknowledged per (project, blob).
func chunkProgressKey(projectID, hash string) string { return projectID + "/" + hash }

func (s *HTTPMemoryStore) chunkProgress(projectID, hash string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chunksDone[chunkProgressKey(projectID, hash)]
}

// setChunkProgress records done pieces; done < 0 forgets the blob.
func (s *HTTPMemoryStore) setChunkProgress(projectID, hash string, done int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chunkProgressKey(projectID, hash)
	if done <= 0 {
		delete(s.chunksDone, key)
		return
	}
	if s.chunksDone == nil || len(s.chunksDone) >= 64 {
		s.chunksDone = map[string]int{}
	}
	s.chunksDone[key] = done
}
