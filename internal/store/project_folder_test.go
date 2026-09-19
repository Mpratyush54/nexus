package store

import "testing"

func TestValidProjectFolderName(t *testing.T) {
	ok := []string{"central-memory", "nexus", "my-app"}
	for _, n := range ok {
		if !ValidProjectFolderName(n) {
			t.Fatalf("expected valid: %q", n)
		}
	}
	bad := []string{"", ".", "..", `\`, `/`, "C:", "D:", `foo/bar`, `foo\bar`, "users", "Desktop", "Documents"}
	for _, n := range bad {
		if ValidProjectFolderName(n) {
			t.Fatalf("expected invalid: %q", n)
		}
	}
}

func TestSanitizeProjectFolderName(t *testing.T) {
	if _, err := SanitizeProjectFolderName("", "", `\`); err == nil {
		t.Fatal("expected error for drive-root folder with no git identity")
	}
	got, err := SanitizeProjectFolderName("https://github.com/acme/widget.git", "", `\`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "widget" {
		t.Fatalf("got %q want widget", got)
	}
}
