//go:build windows

package config

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	in := []byte(token)
	inBlob := windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	var outBlob windows.DataBlob
	if err := windows.CryptProtectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob); err != nil {
		return "", fmt.Errorf("config: protect token: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	raw := unsafe.Slice(outBlob.Data, outBlob.Size)
	return base64.StdEncoding.EncodeToString(raw), nil
}

func unprotectToken(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("config: decode protected token: %w", err)
	}
	if len(raw) == 0 {
		return "", nil
	}
	inBlob := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var outBlob windows.DataBlob
	if err := windows.CryptUnprotectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob); err != nil {
		return "", fmt.Errorf("config: unprotect token: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	return string(unsafe.Slice(outBlob.Data, outBlob.Size)), nil
}
