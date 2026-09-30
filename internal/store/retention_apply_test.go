package store

import (
	"testing"
	"time"

	"central-memory/internal/capture"
)

func TestApplyRetention(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	newerOld := old.Add(2 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)
	drop := ApplyRetention([]capture.VersionStamp{
		{ID: "a", At: old},
		{ID: "b", At: newerOld},
		{ID: "c", At: recent},
		{ID: "d", At: now},
	}, now)
	if len(drop) != 1 || drop[0] != "a" {
		t.Fatalf("delete = %v", drop)
	}
	if drop := ApplyRetention([]capture.VersionStamp{{ID: "c", At: recent}}, now); len(drop) != 0 {
		t.Fatalf("recent should be kept, delete = %v", drop)
	}
	if drop := ApplyRetention(nil, now); len(drop) != 0 {
		t.Fatalf("empty = %v", drop)
	}
}
