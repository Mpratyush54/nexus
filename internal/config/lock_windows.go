//go:build windows

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

const configLockTimeout = 5 * time.Second

func withConfigLock(fn func() error) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	lockPath := filepath.Join(dir, ConfigFileName+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("config: open lock: %w", err)
	}
	defer f.Close()

	deadline := time.Now().Add(configLockTimeout)
	for {
		overlapped := new(windows.Overlapped)
		err = windows.LockFileEx(
			windows.Handle(f.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return fmt.Errorf("config: lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("config: lock timeout after %s", configLockTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
	return fn()
}
