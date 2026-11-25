package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// DesktopIdentity persists a separate device credential under the same OS
// protection as the login token (DPAPI on Windows). It is never a browser token.
func DesktopIdentity() (id, secret string, err error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	err = withConfigLock(func() error {
		cfg, e := LoadFile()
		if e != nil {
			return e
		}
		id, secret = cfg.DesktopDeviceID, cfg.DesktopDeviceKey
		if id != "" && len(secret) >= 32 {
			return nil
		}
		b := make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			return e
		}
		secret = hex.EncodeToString(b)
		if _, e = rand.Read(b[:16]); e != nil {
			return e
		}
		id = "desktop-" + hex.EncodeToString(b[:16])
		cfg.DesktopDeviceID, cfg.DesktopDeviceKey = id, secret
		return writeFile(cfg)
	})
	return
}

func SetDesktopExecution(enabled bool) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	return withConfigLock(func() error {
		cfg, e := LoadFile()
		if e != nil {
			return e
		}
		cfg.DesktopExecutionEnabled = enabled
		return writeFile(cfg)
	})
}

func decodeDesktopKey(cfg *File) error {
	if cfg.DesktopDeviceKeyProtected == "" {
		return nil
	}
	key, err := unprotectToken(cfg.DesktopDeviceKeyProtected)
	if err != nil {
		return fmt.Errorf("config: decrypt desktop identity: %w", err)
	}
	cfg.DesktopDeviceKey = key
	return nil
}
func encodeDesktopKey(cfg *File) error {
	cfg.DesktopDeviceKeyProtected = ""
	if secret := strings.TrimSpace(cfg.DesktopDeviceKey); secret != "" {
		protected, err := protectToken(secret)
		if err != nil {
			return err
		}
		cfg.DesktopDeviceKeyProtected = protected
		if protected != "" {
			cfg.DesktopDeviceKey = ""
		}
	}
	return nil
}
