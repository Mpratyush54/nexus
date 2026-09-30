//go:build windows

package firstrun

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// DetectDaemonAutostart lists HKCU Run values whose data (or name) mentions
// nexus-daemon. When NEXUS_FIRSTRUN_AUTOSTART_FILE is set, that file is used
// instead (same format as the non-Windows stub) so tests stay portable.
func DetectDaemonAutostart() ([]AutostartHit, error) {
	if path := strings.TrimSpace(os.Getenv(EnvAutostartFile)); path != "" {
		return readAutostartFile(path)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil, nil
		}
		return nil, fmt.Errorf("firstrun: open HKCU Run: %w", err)
	}
	defer k.Close()

	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, fmt.Errorf("firstrun: list HKCU Run: %w", err)
	}
	var hits []AutostartHit
	for _, name := range names {
		val, _, err := k.GetStringValue(name)
		if err != nil {
			continue
		}
		if mentionsDaemon(val) || mentionsDaemon(name) {
			hits = append(hits, AutostartHit{Name: name, Value: val})
		}
	}
	return hits, nil
}

// ClearDaemonAutostart deletes HKCU Run values that mention nexus-daemon.
func ClearDaemonAutostart() (int, error) {
	if path := strings.TrimSpace(os.Getenv(EnvAutostartFile)); path != "" {
		return clearAutostartFile(path)
	}
	hits, err := DetectDaemonAutostart()
	if err != nil {
		return 0, err
	}
	if len(hits) == 0 {
		return 0, nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return 0, fmt.Errorf("firstrun: open HKCU Run for write: %w", err)
	}
	defer k.Close()
	removed := 0
	for _, h := range hits {
		if err := k.DeleteValue(h.Name); err != nil && err != registry.ErrNotExist {
			return removed, fmt.Errorf("firstrun: delete Run %q: %w", h.Name, err)
		}
		removed++
	}
	return removed, nil
}

func mentionsDaemon(s string) bool {
	return strings.Contains(strings.ToLower(s), "nexus-daemon")
}

// File helpers shared with the stub path when EnvAutostartFile is set.
func readAutostartFile(path string) ([]AutostartHit, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var hits []AutostartHit
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		value := strings.TrimSpace(line[i+1:])
		if mentionsDaemon(value) || mentionsDaemon(name) {
			hits = append(hits, AutostartHit{Name: name, Value: value})
		}
	}
	return hits, nil
}

func clearAutostartFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var keep []string
	removed := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			keep = append(keep, line)
			continue
		}
		name := strings.TrimSpace(line[:i])
		value := strings.TrimSpace(line[i+1:])
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
