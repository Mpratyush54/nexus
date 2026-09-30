// Premium end-to-end encryption (D14 / P6 stub).
//
// When Enabled is true, the server must not unwrap data keys: device-held
// user keys wrap secrets instead of KMS, so Box.Unwrap refuses with a clear
// error. Off by default (KMS-at-rest remains the default).
package secrets

import "errors"

// Enabled turns on premium E2E mode for this process. Off by default.
var Enabled bool

// ErrE2EEnabled is returned when the server is asked to unwrap a data key
// while premium end-to-end encryption is on.
var ErrE2EEnabled = errors.New("e2e enabled: server cannot decrypt")
