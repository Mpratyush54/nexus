package teleport

import "testing"

func TestRemapAndRedact(t *testing.T) {
	got := Remap(`C:\Users\a\src\app`, "windows", "mac", `C:\Users\a`, "/Users/a")
	if got != "/Users/a/src/app" {
		t.Fatalf("remap = %q", got)
	}
	preview := RedactPreview("mail ada@ex.com from C:\\Users\\a\\notes", `C:\Users\a`)
	if preview != "mail [redacted-email] from ~\\notes" {
		t.Fatalf("preview = %q", preview)
	}
}
