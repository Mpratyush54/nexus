package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptImagesListAndDecodeLocally(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	cache := t.TempDir()
	original := transcriptImageCache
	transcriptImageCache = func() (string, error) { return cache, nil }
	defer func() { transcriptImageCache = original }()
	id := "0f8fad5b-d9cb-469f-a165-70867728950e"
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}},
	}}})
	path := filepath.Join(config, "projects", "D--p", id+".jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"agent": "claude", "native_id": id})
	out, err := Call(context.Background(), "transcript.images", args, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["local"] != true {
		t.Fatalf("transcript not found: %+v", out)
	}
	args, _ = json.Marshal(map[string]any{"agent": "claude", "native_id": id, "offset": 0, "index": 0})
	got, err := Call(context.Background(), "transcript.image", args, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	file := got.(map[string]any)["path"].(string)
	if data, _ := os.ReadFile(file); string(data) != string(png) || filepath.Dir(file) != cache {
		t.Fatalf("decoded image at %s is wrong", file)
	}
	// A second open reuses the cached file.
	again, err := Call(context.Background(), "transcript.image", args, Deps{})
	if err != nil || again.(map[string]any)["path"] != file {
		t.Fatalf("cache miss: %v %+v", err, again)
	}
	args, _ = json.Marshal(map[string]any{"agent": "claude", "native_id": "../../x"})
	out, err = Call(context.Background(), "transcript.images", args, Deps{})
	if err != nil || out.(map[string]any)["local"] != false {
		t.Fatalf("unsafe id must find nothing: %v %+v", err, out)
	}
}
