package main

import (
	"strings"
	"testing"
)

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		remote, local string
		want          bool
	}{
		{"0.3.1", "0.3.0", true},
		{"0.3.0", "0.3.1", false},
		{"0.3.0", "0.3.0", false},
		{"v0.4.0", "0.3.9", true},
		{"0.0.0-48c0d88", "0.3.0", false}, // CI build must not clobber release
		{"0.3.1", "0.0.0-abc", true},
		{"0.0.0-bbb", "0.0.0-aaa", true},
		{"0.3.1", "dev", true},
		{"", "0.3.0", false},
		{"0.3.0", "", true},
	}
	for _, tc := range cases {
		got := versionNewer(tc.remote, tc.local)
		if got != tc.want {
			t.Errorf("versionNewer(%q,%q)=%v want %v", tc.remote, tc.local, got, tc.want)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	cmp, ok := compareSemver("0.3.1", "0.3.0")
	if !ok || cmp <= 0 {
		t.Fatalf("0.3.1 vs 0.3.0 => %d ok=%v", cmp, ok)
	}
	cmp, ok = compareSemver("1.0.0", "1.0.0")
	if !ok || cmp != 0 {
		t.Fatalf("equal => %d ok=%v", cmp, ok)
	}
}

func TestBuildApplyUpdateScript(t *testing.T) {
	script := buildApplyUpdateScript(`C:\stage`, `C:\bin`, []string{"nexus-desktop.exe", "nexus-daemon.exe", "nexus.exe"}, 4242, "0.3.1")
	for _, need := range []string{
		"$selfPid = 4242",
		"nexus-daemon",
		"Stop-Process",
		"Copy-Item",
		"nexus-desktop.exe",
		"0.3.1",
		"Move-Item",
		"version",
	} {
		if !strings.Contains(script, need) {
			t.Fatalf("script missing %q\n%s", need, script)
		}
	}
	if strings.Contains(script, "copy /Y") {
		t.Fatal("must not use silent cmd copy")
	}
}

func TestVersionLabelPhases(t *testing.T) {
	globalUpdate.set(updateIdle, "")
	globalUpdate.setRemote("")
	if !strings.Contains(versionLabel(), "Version:") {
		t.Fatal(versionLabel())
	}
	globalUpdate.set(updateAvailable, "x")
	globalUpdate.setRemote("0.3.1")
	if !strings.Contains(versionLabel(), "0.3.1") {
		t.Fatal(versionLabel())
	}
	globalUpdate.set(updateIdle, "")
}
