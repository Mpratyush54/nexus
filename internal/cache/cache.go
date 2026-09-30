// Package cache is the disposable read cache (spec 7.2). It is safe to delete.
package cache

import "sync"

// Cache stores opaque blobs keyed by string. Clear drops everything.
type Cache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (c *Cache) init() {
	if c.data == nil {
		c.data = map[string][]byte{}
	}
}

// Put stores a copy of val.
func (c *Cache) Put(key string, val []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.data[key] = append([]byte(nil), val...)
}

// Get returns a copy, or nil when missing.
func (c *Cache) Get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	v, ok := c.data[key]
	if !ok {
		return nil
	}
	return append([]byte(nil), v...)
}

// Clear drops every entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = map[string][]byte{}
}

// Len is the number of keys.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}
