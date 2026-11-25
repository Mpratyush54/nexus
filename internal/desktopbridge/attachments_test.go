package desktopbridge

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)

func pngAttachment(name string) Attachment {
	return Attachment{Name: name, MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(testPNG)}
}

func TestPrepareAttachmentsValidatesCountSizeAndContent(t *testing.T) {
	base := Request{ID: "j", DeviceID: "d", ProjectID: "p", SessionID: "s", Kind: "turn", Agent: "claude", Prompt: "look"}
	ok := base
	ok.Attachments = []Attachment{pngAttachment(`..\..\evil/name.png`)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ok.PrepareAttachments(); err != nil {
		t.Fatal(err)
	}
	a := ok.Attachments[0]
	if a.Size != len(testPNG) || len(a.SHA256) != 64 || strings.ContainsAny(a.Name, `/\`) {
		t.Fatalf("prepared %+v", a)
	}
	five := base
	for i := 0; i < 5; i++ {
		five.Attachments = append(five.Attachments, pngAttachment("x.png"))
	}
	if five.Validate() == nil {
		t.Fatal("five images accepted")
	}
	big := base
	big.Attachments = []Attachment{{Name: "big.png", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(append(testPNG, make([]byte, MaxAttachmentBytes)...))}}
	if err := big.PrepareAttachments(); err == nil || !strings.Contains(err.Error(), "5 MB") {
		t.Fatalf("oversized image: %v", err)
	}
	lying := base
	lying.Attachments = []Attachment{{Name: "a.png", MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(testPNG)}}
	if lying.PrepareAttachments() == nil {
		t.Fatal("mismatched media type accepted")
	}
	exe := base
	exe.Attachments = []Attachment{{Name: "a.png", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("MZ\x90\x00 program"))}}
	if exe.PrepareAttachments() == nil {
		t.Fatal("non-image accepted")
	}
	svg := base
	svg.Attachments = []Attachment{{Name: "a.svg", MediaType: "image/svg+xml", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))}}
	if svg.PrepareAttachments() == nil {
		t.Fatal("svg accepted")
	}
	models := Request{ID: "m", DeviceID: "d", ProjectID: "p", Kind: "models", Agent: "claude", Attachments: []Attachment{pngAttachment("a.png")}}
	if models.PrepareAttachments() == nil {
		t.Fatal("models request with images accepted")
	}
}

func TestMemoryClaimHandsImagesOutOnce(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()
	_ = m.Register(ctx, Device{ID: "dev", Owner: "o", KeyHash: HashKey(strings.Repeat("a", 64)), Projects: []string{"p"}})
	req := Request{ID: "j", DeviceID: "dev", ProjectID: "p", SessionID: "s", Kind: "turn", Agent: "claude", Prompt: "look", Attachments: []Attachment{pngAttachment("a.png")}}
	if err := req.PrepareAttachments(); err != nil {
		t.Fatal(err)
	}
	job := Job{Request: req, Owner: "o", State: "queued", Revision: 1, Created: now, Expires: now.Add(time.Minute)}
	if _, err := m.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	// A retried create with the same images (by checksum) is idempotent.
	retry := job
	retry.Attachments = WithoutAttachmentData(job.Attachments)
	if _, err := m.Create(ctx, retry); err != nil {
		t.Fatal("idempotent retry", err)
	}
	if public := job.Public(); public.Attachments[0].Data != "" || public.Attachments[0].SHA256 == "" {
		t.Fatal("public view leaks image bytes")
	}
	claimed, err := m.Claim(ctx, "o", "dev", now)
	if err != nil || claimed == nil || claimed.Attachments[0].Data == "" {
		t.Fatalf("worker must receive the image: %v", err)
	}
	stored, _ := m.Get(ctx, "o", "j")
	if stored.Attachments[0].Data != "" {
		t.Fatal("image bytes kept after the claim")
	}
}
