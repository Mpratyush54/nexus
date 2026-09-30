package desktopui

import (
	"testing"

	"central-memory/internal/cloudclient"
)

func TestFilterMemoryItemsStatusAndCategory(t *testing.T) {
	items := []cloudclient.MemoryItem{
		{Key: "a", Status: "PROPOSED", Category: "auth"},
		{Key: "b", Status: "CONFIRMED", Category: "api"},
		{Key: "c", Status: "ACTIVE", Category: ""},
		{Key: "d", Status: "PROPOSED", Category: "architecture"},
	}

	got := filterMemoryItems(items, "PROPOSED", "")
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "d" {
		t.Fatalf("proposed: %+v", got)
	}

	got = filterMemoryItems(items, "CONFIRMED", "")
	if len(got) != 2 {
		t.Fatalf("confirmed want 2 got %+v", got)
	}
	for _, it := range got {
		if it.Status == "PROPOSED" {
			t.Fatalf("confirmed leaked proposed: %+v", it)
		}
	}

	got = filterMemoryItems(items, "", "general")
	if len(got) != 1 || got[0].Key != "c" {
		t.Fatalf("empty category → general: %+v", got)
	}

	got = filterMemoryItems(items, "PROPOSED", "auth")
	if len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("combined: %+v", got)
	}
}

func TestPreviewSplitWiderThanHalf(t *testing.T) {
	// Right pane should be the majority (≥55%): offset is left fraction.
	if previewSplit >= 0.5 {
		t.Fatalf("previewSplit=%v want left content < 50%% so preview is larger", previewSplit)
	}
	if previewMinW < 400 {
		t.Fatalf("previewMinW=%v too narrow", previewMinW)
	}
}
