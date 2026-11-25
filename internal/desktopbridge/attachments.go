package desktopbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ProtocolVersion is what this build of the relay speaks. Desktops send it
// on every call in ProtocolHeader; servers record it on the device.
//
//	1 (or no header): text turns only.
//	2: turn requests may carry image attachments.
const ProtocolVersion = 2

// ProtocolImages is the first protocol version that accepts attachments.
const ProtocolImages = 2

// ProtocolHeader carries the desktop's protocol version. A header, not a
// body field, so older servers (which reject unknown JSON fields) still
// accept a newer desktop's registration.
const ProtocolHeader = "X-Nexus-Desktop-Protocol"

const (
	MaxAttachments     = 4
	MaxAttachmentBytes = 5 << 20
	// MaxCreateBodyBytes bounds a job request with every attachment at its
	// limit (base64 adds a third) plus the prompt and JSON framing.
	MaxCreateBodyBytes = MaxAttachments*(MaxAttachmentBytes*4/3+4) + 256<<10
	// MaxClaimResponseBytes bounds what a worker reads when claiming a job.
	MaxClaimResponseBytes = MaxCreateBodyBytes + 64<<10
)

// ErrDesktopTooOld is returned when a request needs a newer desktop.
var ErrDesktopTooOld = errors.New("Update Nexus on your computer to send images")

// Attachment is one image a browser sends with a turn. Data is base64 and
// travels only in the create request and the worker's claim response; every
// other response carries the metadata alone.
type Attachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      string `json:"data,omitempty"`
	Size      int    `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// sniffImage returns the media type of supported image bytes, "" otherwise.
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

func cleanName(name string, i int) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == 0x5C || r == ':' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	if name == "" || strings.Trim(name, ".") == "" {
		name = fmt.Sprintf("image-%d", i+1)
	}
	return name
}

// DecodeAttachment checks one attachment and returns its bytes. The content
// must really be the image type it claims; size is checked before decoding.
func DecodeAttachment(a Attachment) ([]byte, error) {
	switch a.MediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, errors.New("attach PNG, JPEG, GIF, or WebP images")
	}
	if base64.StdEncoding.DecodedLen(len(a.Data)) > MaxAttachmentBytes+3 {
		return nil, fmt.Errorf("each image must be %d MB or smaller", MaxAttachmentBytes>>20)
	}
	data, err := base64.StdEncoding.DecodeString(a.Data)
	if err != nil || len(data) == 0 {
		return nil, errors.New("an image could not be read; attach it again")
	}
	if len(data) > MaxAttachmentBytes {
		return nil, fmt.Errorf("each image must be %d MB or smaller", MaxAttachmentBytes>>20)
	}
	if sniffImage(data) != a.MediaType {
		return nil, errors.New("an attachment is not the image type it claims to be")
	}
	return data, nil
}

// PrepareAttachments validates every attachment and fills in the values the
// server computes (clean name, size, checksum). Call after Validate.
func (r *Request) PrepareAttachments() error {
	if len(r.Attachments) == 0 {
		r.Attachments = nil
		return nil
	}
	if r.Kind != "turn" {
		return errors.New("only messages can carry images")
	}
	if len(r.Attachments) > MaxAttachments {
		return fmt.Errorf("attach at most %d images", MaxAttachments)
	}
	for i := range r.Attachments {
		data, err := DecodeAttachment(r.Attachments[i])
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		r.Attachments[i].Name = cleanName(r.Attachments[i].Name, i)
		r.Attachments[i].Size = len(data)
		r.Attachments[i].SHA256 = hex.EncodeToString(sum[:])
	}
	return nil
}

// WithoutAttachmentData returns attachment metadata only.
func WithoutAttachmentData(in []Attachment) []Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]Attachment, len(in))
	for i, a := range in {
		a.Data = ""
		out[i] = a
	}
	return out
}

// Public is the job as browsers see it: never the image bytes.
func (j Job) Public() Job {
	j.Attachments = WithoutAttachmentData(j.Attachments)
	return j
}

// SameRequest reports whether a retried create is the same request.
// Attachments compare by checksum, so a retry need not resend identical data.
func SameRequest(a, b Request) bool {
	if a.ID != b.ID || a.DeviceID != b.DeviceID || a.SessionID != b.SessionID || a.ProjectID != b.ProjectID ||
		a.Kind != b.Kind || a.Agent != b.Agent || a.Model != b.Model || a.Prompt != b.Prompt || len(a.Attachments) != len(b.Attachments) {
		return false
	}
	for i := range a.Attachments {
		if a.Attachments[i].SHA256 != b.Attachments[i].SHA256 || a.Attachments[i].MediaType != b.Attachments[i].MediaType {
			return false
		}
	}
	return true
}
