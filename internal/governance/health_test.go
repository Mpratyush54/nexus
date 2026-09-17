package governance

import (
	"testing"
	"time"
)

func TestStaleFlag(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	stale := MemoryDTO{Key: "old/weak", Confidence: 0.2, LastActive: now.AddDate(0, 0, -200)}
	if !IsStale(stale, now) {
		t.Fatal("conf 0.2 idle 200d must flag stale")
	}
	// Weak but actively used → stays live.
	fresh := MemoryDTO{Key: "fresh/weak", Confidence: 0.1, LastActive: now.Add(-time.Hour)}
	if IsStale(fresh, now) {
		t.Fatal("recently used memory must not flag even with low confidence")
	}
	// Strong but dormant → stays live (decay keeps it above 0.2).
	strong := MemoryDTO{Key: "old/strong", Confidence: 1.0, LastActive: now.AddDate(0, 0, -200)}
	if IsStale(strong, now) {
		t.Fatal("high-confidence dormant memory must not flag")
	}
	batch := FlagStaleBatch([]MemoryDTO{stale, fresh, strong}, now)
	if len(batch) != 1 || batch[0].Key != "old/weak" {
		t.Fatalf("FlagStaleBatch = %v, want [old/weak]", batch)
	}
}

func TestArchivalFilter(t *testing.T) {
	mems := []MemoryDTO{
		{Key: "a", Status: "SUPERSEDED"},
		{Key: "b", Status: "superseded"},
		{Key: "c", Status: "CONFIRMED"},
		{Key: "d", Status: "ARCHIVED"},
		{Key: "e", Status: "PROPOSED"},
	}
	got := SelectForArchive(mems)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("SelectForArchive = %v, want [a b]", got)
	}
}

func TestAnalytics(t *testing.T) {
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	stamps := []time.Time{day, day.Add(time.Hour), day.Add(24 * time.Hour)}
	counts := DailyEventCounts(stamps)
	if counts["2026-09-17"] != 2 || counts["2026-09-18"] != 1 {
		t.Fatalf("DailyEventCounts = %v", counts)
	}
	if g := GrowthPerDay([]int{100, 130}, 10); g != 3 {
		t.Fatalf("GrowthPerDay = %v, want 3", g)
	}
	if g := RelativeGrowth(100, 130); g != 0.3 {
		t.Fatalf("RelativeGrowth = %v, want 0.3", g)
	}
	if a := ExtractionAccuracy(3, 4); a != 0.75 {
		t.Fatalf("ExtractionAccuracy = %v, want 0.75", a)
	}
	if ExtractionAccuracy(0, 0) != 0 {
		t.Fatal("zero proposed must yield 0 accuracy, not NaN")
	}
}
