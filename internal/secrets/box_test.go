package secrets

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableBoxFromEnv(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	t.Setenv("NEXUS_DEV_KMS_KEY", base64.StdEncoding.EncodeToString(master))
	b1, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	plain, wrapped, err := b1.Generate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := b2.Unwrap(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("unwrap mismatch across NewBox calls")
	}
}

func TestNewBoxFallsBackToRandom(t *testing.T) {
	t.Setenv("NEXUS_DEV_KMS_KEY", "")
	b1, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	_, wrapped, err := b1.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Unwrap(wrapped); err == nil {
		t.Fatal("random masters must not unwrap each other's keys")
	}
}

func TestDetectSecretPaths(t *testing.T) {
	paths := []string{
		"src/main.go",
		".env",
		".env.local",
		".env.example",
		"certs/server.pem",
		"id_rsa",
		"docs/readme.md",
		"config/credentials.json",
	}
	got := DetectSecretPaths(paths)
	want := map[string]bool{".env": true, ".env.local": true, "certs/server.pem": true, "id_rsa": true, "config/credentials.json": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected %s in %v", p, got)
		}
	}
	if DetectSecretPath(".env.example") {
		t.Fatal(".env.example is not a secret path")
	}
}

func TestRestoreFile(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plain := []byte("SECRET=value\n")
	ct, err := Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", ".env")
	if err := RestoreFile(dest, ct, key); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("restored %q", got)
	}
}
