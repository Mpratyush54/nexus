package memoryact

import (
	"strings"
	"testing"
)

func TestMemoryPromoteRedaction(t *testing.T) {
	t.Run("strips email phone and home paths", func(t *testing.T) {
		in := "Email ada@example.com or call 555-123-4567; notes live in /home/ada/proj and /Users/ada/src and C:\\Users\\ada\\w and ~/work/notes today."
		got := RedactForPromotion(in)
		if got.Held {
			t.Fatal("held = true, want false")
		}
		for _, leak := range []string{"ada@example.com", "555-123-4567", "/home/ada", "/Users/ada", `C:\Users\ada`, "~/work/notes"} {
			if strings.Contains(got.Text, leak) {
				t.Fatalf("redacted text still contains %q: %s", leak, got.Text)
			}
		}
		for _, placeholder := range []string{RedactedEmail, RedactedPhone, RedactedPath} {
			if !strings.Contains(got.Text, placeholder) {
				t.Fatalf("redacted text missing %s: %s", placeholder, got.Text)
			}
		}
	})

	t.Run("holds AKIA key of 16 or more", func(t *testing.T) {
		in := "Staging notes included AKIAIOSFODNN7EXAMPLE for the old deploy user account."
		got := RedactForPromotion(in)
		if !got.Held {
			t.Fatal("held = false, want true")
		}
		if got.Text != in {
			t.Fatalf("held text = %q, want original", got.Text)
		}
	})

	t.Run("does not hold a short AKIA fragment", func(t *testing.T) {
		in := "The token AKIA1234 is too short to be an access key id."
		got := RedactForPromotion(in)
		if got.Held {
			t.Fatal("short AKIA fragment must not hold the fact")
		}
		if got.Text != in {
			t.Fatalf("text changed: %q", got.Text)
		}
	})

	t.Run("holds private key block", func(t *testing.T) {
		in := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
		got := RedactForPromotion(in)
		if !got.Held || got.Text != in {
			t.Fatalf("private key: held=%v text=%q", got.Held, got.Text)
		}
	})

	t.Run("holds password assignment", func(t *testing.T) {
		in := "The unit file sets password=correcthorsebatterystaple for the local broker."
		got := RedactForPromotion(in)
		if !got.Held || got.Text != in {
			t.Fatalf("password: held=%v text=%q", got.Held, got.Text)
		}
	})

	t.Run("password prose is not an assignment", func(t *testing.T) {
		in := "Choose a strong password for the database before the next rotation window."
		got := RedactForPromotion(in)
		if got.Held {
			t.Fatal("prose mentioning password must not hold")
		}
	})
}
