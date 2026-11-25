//go:build !windows

package daemon

import (
	"errors"

	"github.com/fsnotify/fsnotify"
)

// recursiveWatchSupported: elsewhere fsnotify watches folder by folder
// (inotify, and kqueue with its descriptor budget).
const recursiveWatchSupported = false

type recursiveWatcher struct{}

func newRecursiveWatcher() (*recursiveWatcher, error) {
	return nil, errors.New("recursive watching is only implemented on Windows")
}

func (w *recursiveWatcher) Events() <-chan fsnotify.Event { return nil }
func (w *recursiveWatcher) Errors() <-chan error          { return nil }
func (w *recursiveWatcher) Len() int                      { return 0 }
func (w *recursiveWatcher) Covers(string) bool            { return false }
func (w *recursiveWatcher) AddRoot(string) error          { return errors.New("unsupported") }
func (w *recursiveWatcher) Close() error                  { return nil }
