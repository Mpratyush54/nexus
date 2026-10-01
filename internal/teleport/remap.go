// Package teleport remaps paths and redacts previews (spec 9.5, 7.7).
package teleport

import (
	"regexp"
	"strings"
)

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Remap converts an absolute path from one machine to another.
// Homes are replaced when both are set. Separators follow toOS: "windows" or "mac".
func Remap(path, fromOS, toOS, fromHome, toHome string) string {
	path = strings.TrimSpace(path)
	fromHome = strings.TrimRight(strings.TrimSpace(fromHome), `/\`)
	toHome = strings.TrimRight(strings.TrimSpace(toHome), `/\`)
	if fromHome != "" && strings.HasPrefix(strings.ToLower(path), strings.ToLower(fromHome)) {
		rest := path[len(fromHome):]
		rest = strings.TrimLeft(rest, `/\`)
		path = join(toOS, toHome, rest)
	} else if toOS == "windows" {
		path = strings.ReplaceAll(path, "/", `\`)
	} else {
		path = strings.ReplaceAll(path, `\`, "/")
	}
	_ = fromOS
	return path
}

func join(toOS, home, rest string) string {
	if rest == "" {
		return home
	}
	if toOS == "windows" {
		rest = strings.ReplaceAll(rest, "/", `\`)
		return home + `\` + rest
	}
	rest = strings.ReplaceAll(rest, `\`, "/")
	return home + "/" + rest
}

// RedactPreview strips emails and home-directory prefixes from text that
// a recipient is allowed to see before accept.
func RedactPreview(text, home string) string {
	home = strings.TrimRight(strings.TrimSpace(home), `/\`)
	if home != "" {
		text = strings.ReplaceAll(text, home, "~")
		slash := strings.ReplaceAll(home, `\`, "/")
		text = strings.ReplaceAll(text, slash, "~")
	}
	return emailRE.ReplaceAllString(text, "[redacted-email]")
}
