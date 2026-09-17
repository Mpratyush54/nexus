// Package deps keeps the app's external requirements satisfied: git,
// winget, az CLI, restic (code is optional). Check reports presence +
// versions for `mem doctor`; Ensure installs missing tools via winget so
// `mem install --with-deps` bootstraps a fresh machine hands-free.
package deps

import (
	"fmt"
	"os/exec"
	"strings"
)

// Tool describes one external requirement.
type Tool struct {
	Bin     string // executable name
	Winget  string // winget package id ("" = not installable, e.g. built-in)
	Why     string // what needs it
	Must    bool   // false = nice-to-have (warn only)
	Version []string
}

// Required tools in check order.
func Required() []Tool {
	return []Tool{
		{Bin: "git", Winget: "Git.Git", Why: "memory sync to GitHub", Must: true, Version: []string{"--version"}},
		{Bin: "winget", Winget: "", Why: "installs other tools", Must: true, Version: []string{"--version"}},
		{Bin: "az", Winget: "Microsoft.AzureCLI", Why: "Azure login + Key Vault", Must: false, Version: []string{"--version"}},
		{Bin: "restic", Winget: "restic.restic", Why: "encrypted off-device backup", Must: false, Version: []string{"version"}},
		{Bin: "code", Winget: "Microsoft.VisualStudioCode", Why: "apps export (extensions)", Must: false, Version: []string{"--version"}},
	}
}

// Status is one checked tool.
type Status struct {
	Tool    Tool
	Found   bool
	Version string
}

// Check probes every required tool.
func Check() []Status {
	var out []Status
	for _, t := range Required() {
		st := Status{Tool: t}
		if _, err := exec.LookPath(t.Bin); err == nil {
			st.Found = true
			if len(t.Version) > 0 {
				if b, err := exec.Command(t.Bin, t.Version...).Output(); err == nil {
					st.Version = strings.TrimSpace(strings.Split(string(b), "\n")[0])
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// Ensure installs missing installable tools via winget. Returns per-tool results.
func Ensure(dryRun bool) []string {
	var report []string
	for _, st := range Check() {
		switch {
		case st.Found:
			report = append(report, st.Tool.Bin+": present "+st.Version)
		case st.Tool.Winget == "":
			report = append(report, st.Tool.Bin+": MISSING and not auto-installable ("+st.Tool.Why+")")
		default:
			msg := st.Tool.Bin + ": installing " + st.Tool.Winget
			if dryRun {
				report = append(report, "[preview] "+msg)
				continue
			}
			out, err := exec.Command("winget", "install", "--silent", "--accept-package-agreements", "--accept-source-agreements", st.Tool.Winget).CombinedOutput()
			if err != nil {
				report = append(report, st.Tool.Bin+": install FAILED: "+lastLine(string(out)))
				continue
			}
			report = append(report, st.Tool.Bin+": installed")
		}
	}
	return report
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// Summary renders Check for humans (doctor + status).
func Summary() string {
	var b strings.Builder
	for _, st := range Check() {
		mark := "MISSING"
		if st.Found {
			mark = "ok"
		} else if !st.Tool.Must {
			mark = "optional-missing"
		}
		fmt.Fprintf(&b, "  %-8s %-16s %s  (%s)\n", st.Tool.Bin, mark, st.Version, st.Tool.Why)
	}
	return b.String()
}
