package branches

import (
	"context"
	"errors"
	"testing"
)

// stubLoader is an in-memory BranchLoader for enumeration tests.
type stubLoader struct {
	keys  []string
	views map[string]Entry
	err   error
}

func (s stubLoader) Keys(context.Context, string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.keys, nil
}

func (s stubLoader) Read(_ context.Context, _ string, key string) (Entry, error) {
	if e, ok := s.views[key]; ok {
		return e, nil
	}
	return Entry{}, ErrBranchKeyNotFound
}

func TestListBranchContentsEnumeratesSorted(t *testing.T) {
	l := stubLoader{
		keys: []string{"b/key", "a/key", "b/key", "", "gone"},
		views: map[string]Entry{
			"a/key": {Key: "a/key", Content: "A"},
			"b/key": {Key: "b/key", Content: "B"},
		},
	}
	got, err := ListBranchContents(context.Background(), l, "p1", "br1")
	if err != nil {
		t.Fatalf("ListBranchContents: %v", err)
	}
	if len(got) != 2 || got[0].Key != "a/key" || got[1].Key != "b/key" {
		t.Fatalf("got %+v, want sorted [a/key b/key]", got)
	}
}

func TestListBranchContentsSkipsTombstones(t *testing.T) {
	l := stubLoader{keys: []string{"gone"}, views: map[string]Entry{}}
	if got, err := ListBranchContents(context.Background(), l, "p1", "br1"); err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want empty snapshot", got, err)
	}
}

func TestListBranchContentsGuards(t *testing.T) {
	l := stubLoader{keys: []string{"k"}, views: map[string]Entry{"k": {Key: "k"}}}
	if _, err := ListBranchContents(context.Background(), nil, "p1", "br1"); err == nil {
		t.Error("nil loader must fail")
	}
	if _, err := ListBranchContents(context.Background(), l, "p1", ""); err == nil {
		t.Error("empty branch id must fail")
	}
	boom := stubLoader{err: errors.New("db down")}
	if _, err := ListBranchContents(context.Background(), boom, "p1", "br1"); err == nil {
		t.Error("loader error must propagate")
	}
}
