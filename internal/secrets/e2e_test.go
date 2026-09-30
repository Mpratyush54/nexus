package secrets

import (
	"errors"
	"testing"
)

func TestE2EEnabledRefusesUnwrap(t *testing.T) {
	Enabled = true
	t.Cleanup(func() { Enabled = false })

	b, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	_, wrapped, err := b.Generate()
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Unwrap(wrapped)
	if !errors.Is(err, ErrE2EEnabled) {
		t.Fatalf("got %v want %v", err, ErrE2EEnabled)
	}
	if err.Error() != "e2e enabled: server cannot decrypt" {
		t.Fatalf("message=%q", err.Error())
	}
}

func TestE2EDisabledAllowsUnwrap(t *testing.T) {
	Enabled = false
	b, err := NewBox()
	if err != nil {
		t.Fatal(err)
	}
	plain, wrapped, err := b.Generate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Unwrap(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatal("unwrap mismatch")
	}
}
