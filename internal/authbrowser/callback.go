package authbrowser

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AppCallback is the deep link that replaces the loopback listener when
// the desktop app is installed (spec 7.5). No TCP port is opened.
const AppCallback = "nexus://auth/callback"

// RedirectURI returns the app scheme when the desktop app is installed,
// and the existing loopback URL when it is not (device-code is separate).
func RedirectURI(appInstalled bool, loopbackURL string) string {
	if appInstalled {
		return AppCallback
	}
	return loopbackURL
}

// DeviceCode is the sign-in path used when no app is installed (spec 7.5).
type DeviceCode struct {
	UserCode   string
	DeviceCode string
	ExpiresAt  time.Time
}

// NewDeviceCode mints a short user code and a 15-minute device code.
func NewDeviceCode(now time.Time) (DeviceCode, error) {
	user := make([]byte, 4)
	device := make([]byte, 16)
	if _, err := rand.Read(user); err != nil {
		return DeviceCode{}, err
	}
	if _, err := rand.Read(device); err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{
		UserCode:   hex.EncodeToString(user),
		DeviceCode: hex.EncodeToString(device),
		ExpiresAt:  now.UTC().Add(15 * time.Minute),
	}, nil
}
