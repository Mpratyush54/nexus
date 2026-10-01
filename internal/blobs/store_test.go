package blobs

import "testing"

func TestByteaStorePutGetDedup(t *testing.T) {
	s := NewByteaStore()
	body := []byte("hello BYTEA stand-in")
	sum := HashBytes(body)
	up, err := s.Put(sum, body)
	if err != nil || !up {
		t.Fatalf("first put: uploaded=%v err=%v", up, err)
	}
	up, err = s.Put(sum, body)
	if err != nil || up {
		t.Fatalf("second put should dedup: uploaded=%v err=%v", up, err)
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
	got, err := s.Get(sum)
	if err != nil || string(got) != string(body) {
		t.Fatalf("get: %q %v", got, err)
	}
	if _, err := s.Put(sum, []byte("tampered")); err == nil {
		t.Fatal("expected hash mismatch")
	}
}
