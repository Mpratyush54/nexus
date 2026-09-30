package desktopui

import (
	"testing"
	"time"

	"central-memory/internal/cloudclient"
)

func TestHeatLevel(t *testing.T) {
	cases := []struct {
		n, want int
	}{
		{0, 0}, {1, 1}, {2, 2}, {3, 2}, {4, 3}, {6, 3}, {7, 4}, {100, 4},
	}
	for _, c := range cases {
		if got := heatLevel(c.n); got != c.want {
			t.Fatalf("heatLevel(%d)=%d want %d", c.n, got, c.want)
		}
	}
}

func TestBuildHeatCellsSundayAligned(t *testing.T) {
	now := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC) // Wednesday
	days := []cloudclient.HeatDay{
		{Date: "2026-09-30", Count: 4},
		{Date: "2026-09-29", Count: 1},
	}
	cells, total := buildHeatCells(days, now)
	if total != 5 {
		t.Fatalf("total=%d", total)
	}
	if len(cells) == 0 {
		t.Fatal("empty cells")
	}
	// First cell must be a Sunday.
	start, err := time.Parse("2006-01-02", cells[0].Date)
	if err != nil {
		t.Fatal(err)
	}
	if start.Weekday() != time.Sunday {
		t.Fatalf("start weekday %v", start.Weekday())
	}
	last := cells[len(cells)-1]
	if last.Date != "2026-09-30" || last.Count != 4 || last.Level != 3 {
		t.Fatalf("last=%+v", last)
	}
	// Sparse days fill zeros.
	zeros := 0
	for _, c := range cells {
		if c.Count == 0 {
			zeros++
		}
	}
	if zeros < 300 {
		t.Fatalf("expected mostly empty year, zeros=%d len=%d", zeros, len(cells))
	}
}
