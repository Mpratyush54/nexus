// Package transcriptimages finds images inside a local agent transcript
// (Claude Code and Codex JSONL) so the desktop can show them in a chat.
//
// Privacy: capture never uploads image bytes (the harvester drops image
// blocks and skips oversized lines). This package only reads transcripts on
// this computer, for this computer's own chat view; nothing here sends data
// anywhere. Memory: List never decodes base64, and Extract decodes a single
// image with a hard size cap.
package transcriptimages

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/continuex"
)

// MaxLineBytes skips transcript lines bigger than this without parsing them.
const MaxLineBytes = 48 << 20

// MaxImageBytes caps one decoded image.
const MaxImageBytes = 20 << 20

// MaxRefs caps how many images one transcript lists.
const MaxRefs = 400

// Ref locates one image: the transcript line's byte offset (the captured
// turn index for Claude Code) and its position within that line.
type Ref struct {
	Offset int64 `json:"offset"`
	// Position is the line's source position as capture counts it (the
	// end of the line: every earlier line's length without its line ending,
	// plus one). Captured Claude Code turns use it as their index, so the
	// chat can place the image next to its message.
	Position  int64  `json:"position"`
	Index     int    `json:"index"`
	Role      string `json:"role"` // user, assistant, or tool (a tool result)
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"` // decoded size, estimated from base64 length
	At        string `json:"at,omitempty"`
}

var homeDir = os.UserHomeDir

// ValidNativeID accepts only plain session ids, so an id can never walk out
// of an agent's transcript folder.
func ValidNativeID(id string) bool {
	if len(id) < 6 || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Find returns the local transcript for an agent session, or "" when this
// computer does not have it.
func Find(agent, nativeID string) string {
	if !ValidNativeID(nativeID) {
		return ""
	}
	home, err := homeDir()
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "claude", "claude-code", "claude_code":
		root := os.Getenv("CLAUDE_CONFIG_DIR")
		if root == "" {
			root = filepath.Join(home, ".claude")
		}
		matches, _ := filepath.Glob(filepath.Join(root, "projects", "*", nativeID+".jsonl"))
		if len(matches) > 0 {
			return matches[0]
		}
	case "codex":
		root := os.Getenv("CODEX_HOME")
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		found := ""
		suffix := "-" + strings.ToLower(nativeID) + ".jsonl"
		_ = filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), suffix) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		return found
	}
	return ""
}

// image is one image found in a parsed line; data stays base64.
type image struct {
	role, mediaType, data string
}

// List scans a transcript and returns where its images are, without
// decoding any of them.
func List(path string) ([]Ref, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	refs := []Ref{}
	err = eachImageLine(f, func(offset, position int64, line []byte) bool {
		at, images := lineImages(line)
		for i, img := range images {
			refs = append(refs, Ref{Offset: offset, Position: position, Index: i, Role: img.role, MediaType: img.mediaType, Bytes: int64(base64.StdEncoding.DecodedLen(len(img.data))), At: at})
			if len(refs) >= MaxRefs {
				return false
			}
		}
		return true
	})
	return refs, err
}

// Extract decodes one image. It reads only the line at offset.
func Extract(path string, offset int64, index int) (mediaType string, data []byte, err error) {
	if offset < 0 || index < 0 {
		return "", nil, errors.New("invalid image reference")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return "", nil, err
	}
	line, err := readLine(bufio.NewReaderSize(f, 64<<10))
	if err != nil {
		return "", nil, err
	}
	_, images := lineImages(line)
	if index >= len(images) {
		return "", nil, errors.New("the image is no longer in the transcript")
	}
	img := images[index]
	if base64.StdEncoding.DecodedLen(len(img.data)) > MaxImageBytes+3 {
		return "", nil, fmt.Errorf("the image is larger than %d MB", MaxImageBytes>>20)
	}
	data, err = base64.StdEncoding.DecodeString(strings.TrimRight(img.data, "\r\n"))
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(img.data, "=\r\n"))
	}
	if err != nil {
		return "", nil, errors.New("the image data is damaged")
	}
	if len(data) > MaxImageBytes {
		return "", nil, fmt.Errorf("the image is larger than %d MB", MaxImageBytes>>20)
	}
	mediaType = continuex.ImageMediaType(data)
	if mediaType == "" {
		return "", nil, errors.New("not a PNG, JPEG, GIF, or WebP image")
	}
	return mediaType, data, nil
}

var errLineTooLong = errors.New("transcript line too long")

// readLine reads one line up to MaxLineBytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxLineBytes {
			return nil, errLineTooLong
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(buf) > 0 {
			return buf, nil
		}
		return buf, err
	}
}

// eachImageLine calls fn for every line that may hold an image, with the
// line's byte offset. Oversized lines are skipped without being kept.
func eachImageLine(r io.Reader, fn func(offset, position int64, line []byte) bool) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var offset, position, lineLen int64
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice(byte(10))
		lineLen += int64(len(chunk))
		if !tooLong {
			if len(buf)+len(chunk) > MaxLineBytes {
				tooLong, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(buf) == 0 && !tooLong && err == io.EOF {
			return nil
		}
		// Capture counts a line as its length without the line ending,
		// plus one (an overlong line as its raw size).
		if tooLong {
			position += lineLen
		} else {
			position += int64(len(bytes.TrimRight(buf, "\r\n"))) + 1
		}
		if !tooLong && len(buf) > 0 && mayHoldImage(buf) && !fn(offset, position, buf) {
			return nil
		}
		// Offsets count every byte, skipped lines included.
		offset += lineLen
		lineLen, buf, tooLong = 0, buf[:0], false
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func mayHoldImage(line []byte) bool {
	return bytes.Contains(line, []byte(`"image"`)) || bytes.Contains(line, []byte(`"input_image"`)) || bytes.Contains(line, []byte(`data:image/`))
}

// lineImages parses one JSONL record and returns its timestamp and images in
// document order. Only message content is walked (not Claude's duplicate
// toolUseResult copy), so an image is listed once.
func lineImages(line []byte) (string, []image) {
	var rec map[string]any
	if json.Unmarshal(line, &rec) != nil {
		return "", nil
	}
	at, _ := rec["timestamp"].(string)
	role := ""
	var content any
	if msg, ok := rec["message"].(map[string]any); ok {
		role, _ = msg["role"].(string)
		content = msg["content"]
	} else if payload, ok := rec["payload"].(map[string]any); ok {
		role, _ = payload["role"].(string)
		content = payload["content"]
		if content == nil {
			content = payload["output"]
		}
	}
	if role == "" {
		role, _ = rec["type"].(string)
	}
	var out []image
	walk(content, normalizeRole(role), 0, &out)
	return at, out
}

func normalizeRole(role string) string {
	switch strings.ToLower(role) {
	case "user", "human":
		return "user"
	case "tool":
		return "tool"
	}
	return "assistant"
}

func walk(v any, role string, depth int, out *[]image) {
	if depth > 8 || len(*out) >= 32 {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			walk(item, role, depth+1, out)
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case "image":
			if src, ok := t["source"].(map[string]any); ok {
				if kind, _ := src["type"].(string); kind == "base64" {
					media, _ := src["media_type"].(string)
					data, _ := src["data"].(string)
					if data != "" {
						*out = append(*out, image{role: role, mediaType: media, data: data})
					}
				}
				return
			}
			// ACP-style {type:image, mimeType, data}.
			if data, _ := t["data"].(string); data != "" {
				media, _ := t["mimeType"].(string)
				*out = append(*out, image{role: role, mediaType: media, data: data})
			}
			return
		case "input_image", "image_url":
			url, _ := t["image_url"].(string)
			if m, ok := t["image_url"].(map[string]any); ok {
				url, _ = m["url"].(string)
			}
			if media, data, ok := dataURI(url); ok {
				*out = append(*out, image{role: role, mediaType: media, data: data})
			}
			return
		case "tool_result", "function_call_output":
			walk(t["content"], "tool", depth+1, out)
			walk(t["output"], "tool", depth+1, out)
			return
		}
		walk(t["content"], role, depth+1, out)
	}
}

func dataURI(s string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(s, "data:image/") {
		return "", "", false
	}
	head, data, found := strings.Cut(s[len("data:"):], ",")
	if !found || !strings.HasSuffix(head, ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(head, ";base64"), data, true
}
