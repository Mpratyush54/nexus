package secrets

import "regexp"

const (
	// RedactedSecret replaces AWS access key ids and password assignments
	// in transcript text.
	RedactedSecret = "«redacted:secret»"
	// RedactedEmail replaces email addresses in transcript text.
	RedactedEmail = "«redacted:email»"
)

var (
	awsAccessKeyID = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	passwordAssign = regexp.MustCompile(`(?i)\bpassword\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	emailAddr      = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

// RedactText replaces AWS access key ids (AKIA...), password=... assignments,
// and email addresses. Secret hits become «redacted:secret». Emails become
// «redacted:email». Password assignments are replaced first so a value that
// is itself an email does not remain.
func RedactText(s string) string {
	s = passwordAssign.ReplaceAllString(s, RedactedSecret)
	s = awsAccessKeyID.ReplaceAllString(s, RedactedSecret)
	s = emailAddr.ReplaceAllString(s, RedactedEmail)
	return s
}
