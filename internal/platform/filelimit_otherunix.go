//go:build !windows && !darwin

package platform

func perProcessFileCap() uint64 { return 0 }
