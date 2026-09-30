// Package outbox is the local upload spool (spec 7.2). Items stay on disk
// until the cloud acknowledges them.
package outbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Item is one unsent capture record.
type Item struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Body    []byte `json:"body"`
	Acked   bool   `json:"acked"`
	Attempt int    `json:"attempt"`
}

// Spool is the outbox. Dir empty keeps the queue in memory.
type Spool struct {
	mu    sync.Mutex
	dir   string
	items []Item
}

// Open loads an existing spool.json or starts empty.
func Open(dir string) (*Spool, error) {
	s := &Spool{dir: dir}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.items); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Spool) path() string { return filepath.Join(s.dir, "spool.json") }

// Enqueue adds an item and fsyncs when a directory is set.
func (s *Spool) Enqueue(id, kind string, body []byte) error {
	if id == "" || kind == "" {
		return errors.New("outbox: id and kind are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, Item{ID: id, Kind: kind, Body: append([]byte(nil), body...)})
	return s.flushLocked()
}

// Ack marks an item acknowledged so it leaves Pending.
func (s *Spool) Ack(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Acked = true
			return s.flushLocked()
		}
	}
	return errors.New("outbox: unknown id")
}

// Pending returns unacknowledged items in enqueue order.
func (s *Spool) Pending() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if !it.Acked {
			out = append(out, it)
		}
	}
	if out == nil {
		out = []Item{}
	}
	return out
}

func (s *Spool) flushLocked() error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(s.items)
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}
