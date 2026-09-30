package capture

import (
	"testing"
	"time"
)

func TestRegenerable(t *testing.T) {
	node := Markers{PackageJSON: true, PnpmLock: true}
	if ok, rule := Regenerable("node_modules/leftpad", node); !ok || rule != "default:node" {
		t.Fatalf("node_modules: %v %s", ok, rule)
	}
	if ok, _ := Regenerable("config.local.json", node); ok {
		t.Fatal("config.local.json is not regenerable")
	}
	if ok, _ := Regenerable("node_modules", Markers{}); ok {
		t.Fatal("node_modules without a lockfile is uploaded")
	}
	if ok, rule := Regenerable("vendor/modules", Markers{GoMod: true, VendorModules: true}); !ok || rule != "default:vendor" {
		t.Fatalf("vendor: %v %s", ok, rule)
	}
	if ok, _ := Regenerable("vendor", Markers{GoMod: true}); ok {
		t.Fatal("vendor without modules.txt is uploaded")
	}
}

func TestRetainVersions(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	oldDay := now.Add(-100 * 24 * time.Hour)
	newerOld := oldDay.Add(2 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)
	keep := RetainVersions([]VersionStamp{
		{ID: "a", At: oldDay},
		{ID: "b", At: newerOld},
		{ID: "c", At: recent},
		{ID: "d", At: now},
	}, now)
	got := map[string]bool{}
	for _, id := range keep {
		got[id] = true
	}
	if got["a"] || !got["b"] || !got["c"] || !got["d"] {
		t.Fatalf("keep = %v", keep)
	}
}

func TestUsageState(t *testing.T) {
	cap := int64(100)
	if UsageState(79, cap) != "ok" || UsageState(80, cap) != "warn" || UsageState(100, cap) != "full" {
		t.Fatalf("states 79=%s 80=%s 100=%s", UsageState(79, cap), UsageState(80, cap), UsageState(100, cap))
	}
	if UsageState(1, 0) != "ok" {
		t.Fatal("uncapped is ok")
	}
}
