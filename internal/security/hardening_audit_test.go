package security

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAuditValidatePathClasses(t *testing.T) {
	// Every non-symlink corpus case rejected. Lexically-innocent symlink names
	// pass (runtime sandbox owns containment), but a symlink-prefixed path
	// carrying another lexical violation (.. here) is still rejected.
	for _, c := range AdversarialCases() {
		err := ValidatePath(c.Path)
		if c.Category == "symlink" && c.Path != "evil-dir/../../escape.txt" {
			if err != nil {
				t.Errorf("symlink %q must pass lexical check (runtime sandbox owns it): %v", c.Path, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("corpus [%s/%s] %q allowed, want rejection", c.Category, c.Name, c.Path)
		} else if !errors.Is(err, ErrPathRejected) && err.Error() == "" {
			t.Errorf("empty error for %q", c.Path)
		}
	}
	// Spot-check each rule directly.
	for path := range map[string]bool{
		"a\x00b": true, "": true, "   ": true,
		"a:b": true, "C:foo": true,
		"/etc/passwd": true, `C:\x`: true, `\rooted`: true,
		"../x": true, `..\x`: true, "a/../b": true,
		"//s/sh": true, `a\\b`: true,
		"a.txt.": true, "d./x": true, "n ": true,
	} {
		if ValidatePath(path) == nil {
			t.Errorf("ValidatePath(%q) allowed", path)
		}
	}
	for _, ok := range []string{"a/b/c.txt", ".cursorrules", "dir/.hidden", "a-b_c/d.e", "note..md", "a...b/c"} {
		if err := ValidatePath(ok); err != nil {
			t.Errorf("ValidatePath(%q) rejected: %v", ok, err)
		}
	}
}

func TestAuditSecretsFailClosed(t *testing.T) {
	// NOTE: assembled via concatenation so the file holds no literal
	// token-shaped string (secret-scanning push protection).
	secret := "deploy with " + "AKIAIOSFODNN7EXAMPLE now"
	if !ContainsSecret([]byte(secret)) {
		t.Error("AKIA pattern must match")
	}
	if err := EnforceSecret([]byte(secret)); !errors.Is(err, ErrSecretBlocked) {
		t.Errorf("EnforceSecret = %v, want ErrSecretBlocked", err)
	}
	if err := EnforceSecretString("tok " + "ghp_" + "AbC123xYz987qWeRtYUiOp!"); !errors.Is(err, ErrSecretBlocked) {
		t.Errorf("github token must block: %v", err)
	}
	if ContainsSecret(nil) || ContainsSecret([]byte("")) {
		t.Error("empty input is clean")
	}
	if err := EnforceSecret([]byte("plain notes about lunch")); err != nil {
		t.Errorf("clean content blocked: %v", err)
	}
	if err := EnforceSecret([]byte("")); err != nil {
		t.Errorf("empty must pass: %v", err)
	}
}

func TestAuditVisibilityFailClosed(t *testing.T) {
	if NormalizeVisibility(VisibilityShared) != VisibilityShared {
		t.Error("shared must stay shared")
	}
	for _, v := range []Visibility{"", "private", "Shared", "PUBLIC", "oops"} {
		if NormalizeVisibility(v) != VisibilityPrivate {
			t.Errorf("NormalizeVisibility(%q) must fail closed to private", v)
		}
	}
	if !CanAccess("u1", "u1", VisibilityPrivate) {
		t.Error("owner always passes")
	}
	if CanAccess("", "u1", VisibilityShared) {
		t.Error("anonymous never passes")
	}
	if !CanAccess("u2", "u1", VisibilityShared) {
		t.Error("shared passes non-owner")
	}
	if CanAccess("u2", "u1", VisibilityPrivate) {
		t.Error("private blocks non-owner")
	}
	if CanAccess("u2", "u1", "typo") {
		t.Error("unknown visibility must fail closed")
	}
	if err := EnforceBranchAccess("u2", "u1", VisibilityPrivate); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("enforce = %v, want ErrNotAuthorized", err)
	}
	if err := EnforceBranchAccess("u1", "u1", VisibilityPrivate); err != nil {
		t.Errorf("owner enforce: %v", err)
	}
}

func TestAuditLimiterMath(t *testing.T) {
	// Clamp: non-positive rps/burst -> 1.
	if l := NewLimiter(0, 0); !l.Allow() || l.Allow() {
		t.Error("clamped limiter (1/1) should admit exactly one")
	}
	// Burst consumption then exhaustion.
	l := NewLimiter(10, 3)
	if !l.AllowN(3) {
		t.Fatal("burst of 3 must be admitted")
	}
	if l.Allow() {
		t.Error("exhausted bucket must deny")
	}
	if l.AllowN(0) || l.AllowN(-2) {
		t.Error("non-positive N must be rejected fail-closed")
	}
	// Refill: 10/s -> ~2 tokens after 200ms.
	l2 := NewLimiter(10, 10)
	if !l2.AllowN(10) {
		t.Fatal("drain failed")
	}
	time.Sleep(220 * time.Millisecond)
	if !l2.AllowN(2) {
		t.Error("expected ~2 refilled tokens after 220ms at 10/s")
	}
	// Burst cap: long idle never exceeds burst.
	l3 := NewLimiter(100, 5)
	time.Sleep(50 * time.Millisecond)
	if !l3.AllowN(5) || l3.Allow() {
		t.Error("tokens must cap at burst=5")
	}
	// Presets.
	if e, f := NewEventLimiter(), NewFileOpsLimiter(); e == nil || f == nil {
		t.Error("presets must be non-nil")
	}
	le := NewEventLimiter()
	for i := 0; i < 100; i++ {
		if !le.Allow() {
			t.Fatalf("event limiter denied at %d/100", i)
		}
	}
	if le.Allow() {
		t.Error("event limiter (100/100) must deny #101 immediately")
	}
	lf := NewFileOpsLimiter()
	for i := 0; i < 50; i++ {
		if !lf.Allow() {
			t.Fatalf("fileops limiter denied at %d/50", i)
		}
	}
	if lf.Allow() {
		t.Error("fileops limiter (50/50) must deny #51 immediately")
	}
	// Concurrent safety (token bucket: burst 1000 + 1000/s refill while the
	// test runs, so the bound must include elapsed-time refill + slack).
	lc := NewLimiter(1000, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	start := time.Now()
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if lc.Allow() {
					mu.Lock()
					admitted++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	allowance := 1000 + int(elapsed.Seconds()*1000) + 50
	if admitted > allowance {
		t.Errorf("admitted %d > burst+refill bound %d (burst 1000, 1000/s over %v)", admitted, allowance, elapsed)
	}
}
