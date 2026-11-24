package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	// Go: "main.run(0xc000012345, 0x1)" → "main.run(...)"; "+0x1d" offsets.
	goArgs    = regexp.MustCompile(`\((?:0x[0-9a-fA-F]+|\.\.\.|, |[0-9]+|\{[^}]*\})*\)`)
	hexAddr   = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	goOffset  = regexp.MustCompile(`\s\+0x[0-9a-fA-F]+$`)
	goroutine = regexp.MustCompile(`^goroutine \d+`)
	// JS bundles: drop the origin so the same build fingerprints the same on
	// any host (localhost, preview, production).
	urlOrigin = regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^/\s)]+`)
	digits    = regexp.MustCompile(`\d+`)
	quoted    = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	uuidLike  = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

// Fingerprint groups reports that are the same problem: kind + source +
// app version + the top stack frames. With no recognisable frames it falls
// back to the message with numbers, IDs and quoted values blanked out. The
// result is 16 hex characters.
func Fingerprint(r *Report) string {
	frames := TopFrames(r.Stack, fingerprintFrames)
	basis := strings.Join(frames, "\n")
	if basis == "" {
		basis = normalizeMessage(r.Message)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{r.Kind, r.Source, r.AppVersion, basis}, "\x1f")))
	return hex.EncodeToString(h[:8])
}

// TopFrames returns up to n normalised stack frames from a JS, C#, Go or
// Python style stack, skipping panic/runtime plumbing frames.
func TopFrames(stack string, n int) []string {
	var out []string
	for _, raw := range strings.Split(stack, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || goroutine.MatchString(line) {
			continue
		}
		if !looksLikeFrame(line) || isPlumbing(line) {
			continue
		}
		out = append(out, normalizeFrame(line))
		if len(out) >= n {
			break
		}
	}
	return out
}

func looksLikeFrame(line string) bool {
	switch {
	case strings.HasPrefix(line, "at "): // JS (V8), C#
		return true
	case strings.Contains(line, "@") && (strings.Contains(line, ".js") || strings.Contains(line, ".ts")): // Firefox/Safari
		return true
	case strings.HasPrefix(line, "File \""): // Python
		return true
	case strings.Contains(line, ".go:"): // Go file:line
		return true
	}
	return false
}

func isPlumbing(line string) bool {
	for _, p := range []string{"runtime/panic.go", "runtime/debug/stack.go", "runtime.gopanic", "src/runtime/", "panic(", "/diagnostics/"} {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

func normalizeFrame(line string) string {
	line = goOffset.ReplaceAllString(line, "")
	line = urlOrigin.ReplaceAllString(line, "")
	line = goArgs.ReplaceAllString(line, "(...)")
	line = hexAddr.ReplaceAllString(line, "0x")
	// Query strings on bundle URLs (?v=hash) change per deploy, not per bug.
	if i := strings.IndexByte(line, '?'); i >= 0 {
		if j := strings.IndexAny(line[i:], ":) "); j > 0 {
			line = line[:i] + line[i+j:]
		}
	}
	return line
}

func normalizeMessage(msg string) string {
	msg = strings.TrimSpace(firstLine(msg))
	msg = uuidLike.ReplaceAllString(msg, "<id>")
	msg = quoted.ReplaceAllString(msg, "<q>")
	msg = hexAddr.ReplaceAllString(msg, "<hex>")
	msg = digits.ReplaceAllString(msg, "<n>")
	return strings.ToLower(msg)
}
