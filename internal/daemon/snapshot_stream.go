package daemon

// Streaming snapshot encoding.
//
// A snapshot used to hold its transcript in memory several times over:
// the file (os.ReadFile), a string copy for the secret check, the gzip
// payload, the transcript again after ungzip (grown by io.ReadAll), a
// base64 string and the JSON request body built around it. For a 60 MB
// chat that was ~500 MB of short-lived heap per snapshot, and a live chat
// is snapshotted every few minutes, so the in-process core swung by
// hundreds of megabytes and spent most of its CPU collecting garbage.
//
// Here the transcript is read line by line into the gzip writer, and
// uploads stream base64 straight from the gzip payload into the request.
// Peak memory per snapshot is the compressed payload plus a few buffers.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"central-memory/internal/scan"
)

// openSnapshotTranscript opens the transcript a snapshot packs: the
// transcript file, or for SQLite-only harnesses a JSONL rendering of the
// conversation's rows.
func openSnapshotTranscript(layout SnapshotLayout, workspaceRoot string) (io.ReadSeekCloser, error) {
	path := layout.TranscriptFile
	// Antigravity: prefer full sibling when caller pointed at truncated log.
	if layout.Harness == "antigravity" && path != "" {
		dir := filepath.Dir(path)
		full := filepath.Join(dir, "transcript_full.jsonl")
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			path = full
		} else {
			alt := filepath.Join(dir, "transcript.jsonl")
			if _, err := os.Stat(path); err != nil {
				path = alt
			}
		}
	}
	if path != "" {
		if f, err := os.Open(path); err == nil {
			return f, nil
		}
	}
	// SQLite-only harnesses (Windsurf/Copilot/OpenCode/…): extract dialogue
	// into JSONL at collect time — no permanent sidecar required.
	if layout.SQLiteSource != "" {
		raw, err := extractSnapshotTranscriptJSONL(layout.Harness, layout.SQLiteSource, workspaceRoot, layout.ConversationID)
		if err != nil {
			return nil, err
		}
		return nopSeekCloser{bytes.NewReader(raw)}, nil
	}
	if path != "" {
		return nil, fmt.Errorf("no transcript at %s", path)
	}
	return nil, fmt.Errorf("no transcript path")
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

// forEachLine calls fn for every line of r, including its '\n' (the last
// line may have none). Lines are never truncated; the slice is only valid
// during the call.
func forEachLine(r io.Reader, fn func(line []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			long = append(long, chunk...)
			continue
		}
		line := chunk
		if len(long) > 0 {
			long = append(long, chunk...)
			line = long
		}
		if len(line) > 0 {
			if ferr := fn(line); ferr != nil {
				return ferr
			}
		}
		long = long[:0]
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// hasNeverSecret is security.ContainsSecret without copying b to a string.
func hasNeverSecret(b []byte) bool {
	for _, re := range scan.NeverPatterns {
		if re.Match(b) {
			return true
		}
	}
	return false
}

// snapshotBlockBytes is how much of a transcript is scanned for secrets at
// once: whole lines, so a record is never split, in blocks large enough
// that per-call regexp overhead stays negligible.
const snapshotBlockBytes = 1 << 20

// forEachBlock calls fn with runs of whole lines of r, each about
// snapshotBlockBytes (a longer line is a block of its own). The slice is
// only valid during the call.
func forEachBlock(r io.Reader, fn func(block []byte) error) error {
	block := make([]byte, 0, snapshotBlockBytes+64<<10)
	err := forEachLine(r, func(line []byte) error {
		if len(block) > 0 && len(block)+len(line) > snapshotBlockBytes {
			if err := fn(block); err != nil {
				return err
			}
			block = block[:0]
		}
		block = append(block, line...)
		return nil
	})
	if err == nil && len(block) > 0 {
		err = fn(block)
	}
	return err
}

// gzipSnapshotTranscript compresses the transcript and counts its lines. As
// before, when any part matches a NeverPattern the whole transcript is
// redacted with RedactSecrets (NeverPatterns plus the extra patterns). The
// transcript is read twice in blocks of whole lines instead of being held.
func gzipSnapshotTranscript(src io.ReadSeeker) (gz []byte, lines int, err error) {
	secret := false
	if err := forEachBlock(src, func(block []byte) error {
		if hasNeverSecret(block) {
			secret = true
			return io.EOF // stop early
		}
		return nil
	}); err != nil && err != io.EOF {
		return nil, 0, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	var last byte = '\n'
	err = forEachBlock(src, func(block []byte) error {
		lines += bytes.Count(block, []byte{'\n'})
		last = block[len(block)-1]
		if secret {
			if redacted, ok := RedactSecrets(string(block)); ok {
				block = []byte(redacted)
			}
		}
		_, err := zw.Write(block)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if last != '\n' {
		lines++ // an unterminated last line counts too
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), lines, nil
}

// gzipDigest returns the SHA-256 (hex) and length of gz's decompressed
// content without holding it in memory.
func gzipDigest(gz []byte) (hash string, size int64, err error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", 0, err
	}
	defer zr.Close()
	h := sha256.New()
	size, err = io.Copy(h, zr)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// base64Reader streams the standard base64 encoding of src.
type base64Reader struct {
	src     io.Reader
	in, out []byte
	pending []byte
	err     error
}

func newBase64Reader(src io.Reader) *base64Reader {
	const chunk = 3 * 16 << 10 // a multiple of 3: no padding mid-stream
	return &base64Reader{src: src, in: make([]byte, chunk), out: make([]byte, base64.StdEncoding.EncodedLen(chunk))}
}

func (b *base64Reader) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		n, err := io.ReadFull(b.src, b.in)
		if n > 0 {
			m := base64.StdEncoding.EncodedLen(n)
			base64.StdEncoding.Encode(b.out[:m], b.in[:n])
			b.pending = b.out[:m]
		}
		switch err {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			b.err = io.EOF
		default:
			b.err = err
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

// b64Field is a JSON string field whose value is base64 of a stream.
type b64Field struct {
	name string
	size int64                     // decoded length
	open func() (io.Reader, error) // a fresh stream of the decoded bytes
}

// streamingJSONBody builds a JSON object of fields plus base64 fields
// without materialising the encoded values. It returns the body length and
// a function that opens the body (usable again for http.Request.GetBody).
func streamingJSONBody(fields map[string]any, b64 []b64Field) (int64, func() (io.ReadCloser, error), error) {
	head, err := json.Marshal(fields)
	if err != nil {
		return 0, nil, err
	}
	head = head[:len(head)-1] // drop '}'
	sep := ","
	if len(fields) == 0 {
		sep = ""
	}
	type part struct {
		prefix []byte
		f      b64Field
	}
	parts := make([]part, 0, len(b64))
	length := int64(len(head)) + 1 // + closing '}'
	for _, f := range b64 {
		key, _ := json.Marshal(f.name)
		prefix := []byte(sep + string(key) + `:"`)
		sep = ","
		parts = append(parts, part{prefix: prefix, f: f})
		length += int64(len(prefix)) + int64(base64.StdEncoding.EncodedLen(int(f.size))) + 1
	}
	open := func() (io.ReadCloser, error) {
		readers := []io.Reader{bytes.NewReader(head)}
		for _, p := range parts {
			readers = append(readers, bytes.NewReader(p.prefix))
			if p.f.size > 0 {
				src, err := p.f.open()
				if err != nil {
					return nil, err
				}
				readers = append(readers, newBase64Reader(io.LimitReader(src, p.f.size)))
			}
			readers = append(readers, bytes.NewReader([]byte{'"'}))
		}
		readers = append(readers, bytes.NewReader([]byte{'}'}))
		return io.NopCloser(io.MultiReader(readers...)), nil
	}
	return length, open, nil
}

// bytesField is a b64Field over an in-memory value.
func bytesField(name string, data []byte) b64Field {
	return b64Field{name: name, size: int64(len(data)), open: func() (io.Reader, error) { return bytes.NewReader(data), nil }}
}

// gunzipField is a b64Field over gz's decompressed content of length size.
func gunzipField(name string, gz []byte, size int64) b64Field {
	return b64Field{name: name, size: size, open: func() (io.Reader, error) {
		return gzip.NewReader(bytes.NewReader(gz))
	}}
}
