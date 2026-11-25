//go:build !windows

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
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
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("config: lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("config: lock timeout after %s", configLockTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}
