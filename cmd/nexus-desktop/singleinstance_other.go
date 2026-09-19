//go:build !windows

package main

func acquireSingleInstance() (release func(), ok bool) {
	return func() {}, true
}
