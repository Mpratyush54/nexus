package server

import (
	"central-memory/internal/store"
	"strconv"
	"testing"
)

func TestBackwardSessionPagesKeepLatestAndExactCursors(t *testing.T) {
	base := int(179000000000000000)
	turns := []store.SessionTurn{}
	for i := 0; i < 205; i++ {
		turns = append(turns, store.SessionTurn{Idx: base + i})
	}
	page, next := pageSessionTurnsBefore(turns, nil, 100)
	if len(page) != 100 || page[0].Idx != base+105 || page[99].Idx != base+204 || next != strconv.Itoa(base+105) {
		t.Fatal("latest page/cursor", next)
	}
	before, _ := strconv.Atoi(next)
	page, next = pageSessionTurnsBefore(turns, &before, 100)
	if len(page) != 100 || page[0].Idx != base+5 || page[99].Idx != base+104 || next != strconv.Itoa(base+5) {
		t.Fatal("second page/cursor", next)
	}
	before, _ = strconv.Atoi(next)
	page, next = pageSessionTurnsBefore(turns, &before, 100)
	if len(page) != 5 || page[0].Idx != base || next != "" {
		t.Fatal("earliest page", next)
	}
}
