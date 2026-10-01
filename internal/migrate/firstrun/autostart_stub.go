//go:build !windows

package firstrun

import (
	"bufio"
	"os"
	"strings"
)

// DetectDaemonAutostart reads NEXUS_FIRSTRUN_AUTOSTART_FILE (fake Run key)
// and returns entries whose value mentions nexus-daemon. When the env is
// unset or the file is missing, returns an empty list (no error).
func DetectDaemonAutostart() ([]AutostartHit, error) {
	path := strings.TrimSpace(os.Getenv(EnvAutostartFile))
	if path == "" {
		return nil, nil
	}
	return readAutostartFile(path)
}

// ClearDaemonAutostart rewrites the fake Run file without nexus-daemon hits.
func ClearDaemonAutostart() (int, error) {
	path := strings.TrimSpace(os.Getenv(EnvAutostartFile))
	if path == "" {
		return 0, nil
	}
	hits, err := readAutostartFile(path)
	if err != nil {
		return 0, err
	}
	if len(hits) == 0 {
		return 0, nil
	}
	all, err := readAllAutostartLines(path)
	if err != nil {
		return 0, err
	}
	var keep []string
	removed := 0
	for _, line := range all {
		name, value, ok := splitNameValue(line)
		if !ok {
			keep = append(keep, line)
			continue
		}
		if mentionsDaemon(value) || mentionsDaemon(name) {
			removed++
			continue
		}
		keep = append(keep, name+"="+value)
	}
	body := strings.Join(keep, "\n")
	if len(keep) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return removed, err
	}
	return removed, nil
}

func readAutostartFile(path string) ([]AutostartHit, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var hits []AutostartHit
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := splitNameValue(line)
		if !ok {
			continue
		}
		if mentionsDaemon(value) || mentionsDaemon(name) {
			hits = append(hits, AutostartHit{Name: name, Value: value})
		}
	}
	if err := sc.Err(); err != nil {
		return hits, err
	}
	return hits, nil
}

func readAllAutostartLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" && len(lines) == 0 {
			continue
		}
		// Preserve non-empty logical lines; drop trailing empty from Split.
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func splitNameValue(line string) (name, value string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func mentionsDaemon(s string) bool {
	return strings.Contains(strings.ToLower(s), "nexus-daemon")
}
