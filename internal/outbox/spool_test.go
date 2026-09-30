package outbox

import "testing"

func TestSpoolAckSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue("a", "turn", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue("b", "turn", []byte("yo")); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack("a"); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := again.Pending()
	if len(pending) != 1 || pending[0].ID != "b" || string(pending[0].Body) != "yo" {
		t.Fatalf("pending = %+v", pending)
	}
}
