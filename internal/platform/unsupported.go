//go:build !windows && !darwin && !linux

package platform

import "fmt"

type unsupportedManager struct{}

func currentManager() Manager { return unsupportedManager{} }

func (unsupportedManager) Install(string, []string) error {
	return fmt.Errorf("platform: service install not supported on this OS")
}

func (unsupportedManager) Uninstall() error {
	return fmt.Errorf("platform: service uninstall not supported on this OS")
}

func (unsupportedManager) Status() (Status, error) {
	return StatusUnknown, fmt.Errorf("platform: service status not supported on this OS")
}
