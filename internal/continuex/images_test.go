package continuex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func TestImageMediaTypeSniffsContent(t *testing.T) {
	cases := map[string]string{
		string(tinyPNG):                "image/png",
		"\xFF\xD8\xFF\xE0rest":         "image/jpeg",
		"GIF89a....":                   "image/gif",
		"RIFF\x00\x00\x00\x00WEBPVP8 ": "image/webp",
		"MZ\x90\x00 not an image":      "",
	}
	for in, want := range cases {
		if got := ImageMediaType([]byte(in)); got != want {
			t.Fatalf("%q: got %q want %q", in[:4], got, want)
		}
	}
}

func TestReadImageRejectsRenamedFilesAndOversize(t *testing.T) {
	if _, _, err := ReadImage(writeImage(t, "fake.png", []byte("MZ not really")), MaxInlineImageBytes); err == nil {
		t.Fatal("an executable renamed .png must be refused")
	}
	if _, _, err := ReadImage(writeImage(t, "big.png", append(tinyPNG, make([]byte, 100)...)), 64); err == nil {
		t.Fatal("over the limit must be refused")
	}
	if _, _, err := ReadImage(writeImage(t, "a.txt", tinyPNG), MaxInlineImageBytes); err == nil {
		t.Fatal("unsupported extension must be refused")
	}
	if !AllowedImage(writeImage(t, "anim.gif", []byte("GIF89a...."))) {
		t.Fatal("GIF should be allowed")
	}
}

func TestClaudeSeededPlanSendsImageBlocksOnStdin(t *testing.T) {
	image := writeImage(t, "shot.png", tinyPNG)
	plan, err := SeededPlan("claude", "what is in this screenshot?", "", []string{image})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(plan.Argv, " ")
	if !strings.Contains(argv, "--input-format stream-json") || strings.Contains(argv, "screenshot") {
		t.Fatalf("prompt must go through stdin, argv: %s", argv)
	}
	plan, err = WithModel(plan, "sonnet")
	if err != nil || len(plan.Stdin) == 0 {
		t.Fatalf("model must keep stdin: %v", err)
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(plan.Stdin, &msg); err != nil {
		t.Fatal(err)
	}
	c := msg.Message.Content
	if msg.Type != "user" || len(c) != 2 || c[0].Type != "image" || c[0].Source.MediaType != "image/png" || c[1].Text != "what is in this screenshot?" {
		t.Fatalf("unexpected message %+v", msg)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(c[0].Source.Data); string(decoded) != string(tinyPNG) {
		t.Fatal("image bytes changed")
	}
	if _, err := SeededPlan("claude", "x", "", []string{image, image, image, image, image}); err == nil {
		t.Fatal("five images must be refused")
	}
}

func TestCodexInputListsLocalImagesBeforeText(t *testing.T) {
	input := codexInput(codexMessage{text: "hi", images: []string{"C:/a.png"}})
	b, _ := json.Marshal(input)
	if string(b) != `[{"path":"C:/a.png","type":"localImage"},{"text":"hi","type":"text"}]` {
		t.Fatalf("codex input %s", b)
	}
}

func TestImageTagsNameImagesWithoutBytes(t *testing.T) {
	got := ImageTags("look", []string{`C:\Users\me\shot.png`})
	if !strings.Contains(got, `<image name=[Image #1] path="C:/Users/me/shot.png">`) || !strings.HasPrefix(got, "look") {
		t.Fatalf("tags %q", got)
	}
	if ImageTags("plain", nil) != "plain" {
		t.Fatal("no images: unchanged")
	}
}
