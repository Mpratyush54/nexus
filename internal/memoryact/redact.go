// Package memoryact implements product-spec memory actions that sit beside
// the legacy PROPOSED/CONFIRMED lifecycle: public status, promotion
// redaction, and provenance views.
package memoryact

import "regexp"

// Placeholders replace PII that can be stripped without discarding the fact.
const (
	RedactedEmail = "«redacted:email»"
	RedactedPhone = "«redacted:phone»"
	RedactedPath  = "«redacted:path»"
)

// Secrets that cannot be stripped safely. A hit keeps the fact at session
// level (held) instead of publishing a partially cleaned project fact.
var (
	reAWSAccessKey = regexp.MustCompile(`AKIA[0-9A-Z]{16,}`)
	rePrivateKey   = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
	rePassword     = regexp.MustCompile(`(?i)\b[\w.-]*(?:password|passwd|pwd)\s*[:=]\s*\S+`)
)

// PII that is safe to replace in place.
var (
	reEmail = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	rePhone = regexp.MustCompile(`(?:\+\d{1,3}[\s.\-]?)?(?:\(\d{3}\)|\d{3})[\s.\-]\d{3}[\s.\-]\d{4}\b`)
	reHome  = regexp.MustCompile(`(?:~[/\\][^\s«»]+|/(?:home|Users)/[A-Za-z0-9._-]+(?:[/\\][^\s«»]*)?|[A-Za-z]:[/\\]Users[/\\][A-Za-z0-9._-]+(?:[/\\][^\s«»]*)?)`)
)

// Redaction is the result of scanning one fact before promotion.
type Redaction struct {
	Text string
	Held bool
}

// RedactForPromotion strips emails, phone numbers, and home-directory paths.
// A raw AWS access key (AKIA plus 16 or more key characters), a private-key
// block, or a password assignment cannot be stripped safely: Held is true
// and Text is the original fact.
func RedactForPromotion(text string) Redaction {
	if reAWSAccessKey.MatchString(text) || rePrivateKey.MatchString(text) || rePassword.MatchString(text) {
		return Redaction{Text: text, Held: true}
	}
	out := reEmail.ReplaceAllString(text, RedactedEmail)
	out = rePhone.ReplaceAllString(out, RedactedPhone)
	out = reHome.ReplaceAllString(out, RedactedPath)
	return Redaction{Text: out, Held: false}
}
