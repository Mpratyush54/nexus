package cache

import "testing"

func TestClear(t *testing.T) {
	var c Cache
	c.Put("timeline", []byte("[]"))
	if c.Len() != 1 || string(c.Get("timeline")) != "[]" {
		t.Fatal("put")
	}
	c.Clear()
	if c.Len() != 0 || c.Get("timeline") != nil {
		t.Fatal("clear")
	}
}
