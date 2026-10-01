package secrets

import "testing"

func TestRedactText(t *testing.T) {
	in := "user alice@example.com password=hunter2 key AKIAIOSFODNN7EXAMPLE done"
	got := RedactText(in)
	want := "user «redacted:email» «redacted:secret» key «redacted:secret» done"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if RedactText("Password = \"s3cret\"") != RedactedSecret {
		t.Fatal("quoted password assignment")
	}
	if RedactText("password=alice@example.com") != RedactedSecret {
		t.Fatal("password value that is an email")
	}
	if RedactText("AKIAIOSFODNN7EXAMPL") != "AKIAIOSFODNN7EXAMPL" {
		t.Fatal("short AKIA must stay")
	}
	clean := "no secrets in this sentence"
	if RedactText(clean) != clean {
		t.Fatal("clean text changed")
	}
	if RedactText("bob@corp.io and cara@corp.io") != "«redacted:email» and «redacted:email»" {
		t.Fatal("two emails")
	}
}
