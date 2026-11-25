package continuex

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxLocalImageBytes bounds one image a person attaches on this computer.
const MaxLocalImageBytes = 20 << 20

// MaxInlineImageBytes is the largest image sent inline as base64 (Claude's
// documented per-image limit). The desktop shrinks bigger images first.
const MaxInlineImageBytes = 5 << 20

// MaxImages is how many images one message may carry.
const MaxImages = 4

// ImageMediaType sniffs the file content, not the extension, and returns the
// media type of a supported image ("" for anything else).
func ImageMediaType(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "image/gif"
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// ImageExtension maps a supported media type to a file extension.
func ImageExtension(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

func imageExtensionAllowed(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// ReadImage reads a local image for inline delivery. It refuses files whose
// content is not a supported image and files above limit.
func ReadImage(path string, limit int64) (mediaType string, data []byte, err error) {
	if !imageExtensionAllowed(path) {
		return "", nil, errors.New("attach a PNG, JPEG, GIF, or WebP image")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("the image is no longer available: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", nil, errors.New("the image is no longer available")
	}
	if info.Size() > limit {
		return "", nil, fmt.Errorf("%s is larger than %d MB", filepath.Base(path), limit>>20)
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > limit {
		return "", nil, fmt.Errorf("%s is larger than %d MB", filepath.Base(path), limit>>20)
	}
	mediaType = ImageMediaType(data)
	if mediaType == "" {
		return "", nil, fmt.Errorf("%s is not a PNG, JPEG, GIF, or WebP image", filepath.Base(path))
	}
	return mediaType, data, nil
}

// ClaudeUserMessage builds one Claude Code stream-json user message: the
// text and every image as a base64 image content block.
func ClaudeUserMessage(text string, images []string) (map[string]any, error) {
	if len(images) > MaxImages {
		return nil, fmt.Errorf("attach at most %d images", MaxImages)
	}
	content := []any{}
	for _, path := range images {
		mediaType, data, err := ReadImage(path, MaxInlineImageBytes)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(data),
		}})
	}
	content = append(content, map[string]any{"type": "text", "text": text})
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}}, nil
}

// ImageTags appends the attachment markers Nexus already reads in Codex
// turns, so a recorded message shows its images (desktop thumbnails while
// the files exist, a chip on the web). Only the name and local path are
// recorded; image bytes never leave this computer through the transcript.
func ImageTags(prompt string, images []string) string {
	if len(images) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	for i, path := range images {
		fmt.Fprintf(&b, "\n<image name=[Image #%d] path=%q>\n</image>", i+1, filepath.ToSlash(path))
	}
	return b.String()
}
