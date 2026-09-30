// Package teleport remaps paths, redacts previews, and builds Prepare plans
// (spec 6.5 / 9.4–9.6).
package teleport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PathRemap is one token rewrite shown in the Prepare checklist.
type PathRemap struct {
	Token  string `json:"token"`
	From   string `json:"from"`
	To     string `json:"to"`
	Sample string `json:"sample,omitempty"`
}

// ContinueModeOption is one Continue button from the Prepare checklist (D2).
type ContinueModeOption struct {
	ID    string `json:"id"` // here | open_in_agent | seeded
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// PrepareArgs are dry-run inputs for teleport.prepare.
type PrepareArgs struct {
	SessionID string   `json:"session_id"`
	Root      string   `json:"root"`
	Commit    string   `json:"commit"`
	FromOS    string   `json:"from_os"`
	ToOS      string   `json:"to_os"`
	FromHome  string   `json:"from_home"`
	ToHome    string   `json:"to_home"`
	Agent     string   `json:"agent"`
	Text      string   `json:"text"`
	Home      string   `json:"home"`
	Secrets   []string `json:"secrets"`
}

// PreparePlan is the one-screen Prepare checklist (spec 6.5).
type PreparePlan struct {
	SessionID string `json:"session_id,omitempty"`
	DryRun    bool   `json:"dry_run"`
	Repo      struct {
		Path  string `json:"path"`
		Found bool   `json:"found"`
		Note  string `json:"note,omitempty"`
	} `json:"repo"`
	Commit struct {
		SHA    string `json:"sha,omitempty"`
		Action string `json:"action"` // fetch | present | missing
		Note   string `json:"note,omitempty"`
	} `json:"commit"`
	Target struct {
		Mode          string `json:"mode"` // worktree | checkout
		WorktreePath  string `json:"worktree_path,omitempty"`
		CheckoutClean *bool  `json:"checkout_clean,omitempty"`
		Note          string `json:"note,omitempty"`
	} `json:"target"`
	Agent struct {
		Name  string `json:"name"`
		Found bool   `json:"found"`
		Note  string `json:"note,omitempty"`
	} `json:"agent"`
	Paths    []PathRemap          `json:"paths"`
	Secrets  SecretsPlan          `json:"secrets"`
	Continue []ContinueModeOption `json:"continue"`
	Preview  string               `json:"preview,omitempty"`
}

// SecretsPlan is names-only by default (D8).
type SecretsPlan struct {
	Mode  string   `json:"mode"` // names_only
	Names []string `json:"names,omitempty"`
	Note  string   `json:"note,omitempty"`
}

// BuildPreparePlan returns a dry-run Prepare checklist using local heuristics.
func BuildPreparePlan(args PrepareArgs) PreparePlan {
	var plan PreparePlan
	plan.SessionID = strings.TrimSpace(args.SessionID)
	plan.DryRun = true

	root := strings.TrimSpace(args.Root)
	plan.Repo.Path = root
	if root != "" {
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			plan.Repo.Found = true
			if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
				plan.Repo.Note = "git clone found"
			} else {
				plan.Repo.Note = "directory found; .git missing — clone or open the repo"
			}
		} else {
			plan.Repo.Note = "repo path not found; clone to continue"
		}
	} else {
		plan.Repo.Note = "no workspace root provided"
	}

	sha := strings.TrimSpace(args.Commit)
	plan.Commit.SHA = sha
	switch {
	case sha == "":
		plan.Commit.Action = "missing"
		plan.Commit.Note = "no base commit in args; Prepare will ask the sender for a push or bundle"
	case root != "" && plan.Repo.Found && gitHasCommit(root, sha):
		plan.Commit.Action = "present"
		plan.Commit.Note = "commit already in local object store"
	default:
		plan.Commit.Action = "fetch"
		plan.Commit.Note = "git fetch origin " + shortSHA(sha) + " (or use a git bundle from the manifest)"
	}

	plan.Target.Mode = "worktree"
	if root != "" {
		base := filepath.Base(root)
		plan.Target.WorktreePath = filepath.Join(filepath.Dir(root), base+".teleport", shortID(args.SessionID))
		plan.Target.Note = "default: new worktree so the current checkout is untouched"
		if clean := gitWorkingTreeClean(root); plan.Repo.Found {
			plan.Target.CheckoutClean = &clean
			if !clean {
				plan.Target.Note += "; this checkout is dirty — stash or use the worktree"
			}
		}
	} else {
		plan.Target.Note = "choose a clone, then a worktree target"
	}

	agent := strings.TrimSpace(args.Agent)
	if agent == "" {
		agent = "claude"
	}
	plan.Agent.Name = agent
	if _, err := exec.LookPath(agentBinary(agent)); err == nil {
		plan.Agent.Found = true
		plan.Agent.Note = agent + " found on PATH"
	} else {
		plan.Agent.Found = false
		plan.Agent.Note = "install " + agent + ", or Continue as a seeded session in another agent"
	}

	fromHome := strings.TrimSpace(args.FromHome)
	toHome := strings.TrimSpace(args.ToHome)
	if toHome == "" {
		toHome = strings.TrimSpace(args.Home)
	}
	fromOS := strings.TrimSpace(args.FromOS)
	toOS := strings.TrimSpace(args.ToOS)
	if toOS == "" {
		toOS = "unix"
	}
	sampleFrom := fromHome
	if sampleFrom == "" {
		sampleFrom = "/Users/sender/code/repo"
	}
	samplePath := Remap(filepath.ToSlash(sampleFrom+"/src/main.go"), fromOS, toOS, fromHome, toHome)
	plan.Paths = []PathRemap{
		{Token: "${HOME}", From: fromHome, To: toHome, Sample: samplePath},
		{Token: "${WORKSPACE}", From: rootHint(fromHome, root), To: root, Sample: Remap(rootHint(fromHome, root), fromOS, toOS, fromHome, toHome)},
	}

	names := make([]string, 0, len(args.Secrets))
	for _, n := range args.Secrets {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	plan.Secrets = SecretsPlan{
		Mode:  "names_only",
		Names: names,
		Note:  "secret files restore as name-only templates unless the sender included values",
	}
	if plan.Secrets.Names == nil {
		plan.Secrets.Names = []string{}
	}

	plan.Continue = []ContinueModeOption{
		{ID: "here", Label: "Continue here", Note: "Nexus chat pane drives the agent in the background"},
		{ID: "open_in_agent", Label: "Open in " + agent, Note: "launch the agent's own window already resumed"},
		{ID: "seeded", Label: "Seeded in another agent", Note: "summary + last turns when native resume is unavailable"},
	}

	if strings.TrimSpace(args.Text) != "" || strings.TrimSpace(args.Home) != "" {
		plan.Preview = RedactPreview(args.Text, args.Home)
	}
	return plan
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "session"
	}
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func rootHint(home, root string) string {
	if root != "" {
		return root
	}
	if home != "" {
		return home + "/code/repo"
	}
	return "/Users/sender/code/repo"
}

func agentBinary(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "cursor", "cursor-cli", "agent":
		return "agent"
	case "claude", "claude-code":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	case "copilot":
		return "copilot"
	default:
		return agent
	}
}

func gitHasCommit(root, sha string) bool {
	cmd := exec.Command("git", "-C", root, "cat-file", "-e", sha+"^{commit}")
	return cmd.Run() == nil
}

func gitWorkingTreeClean(root string) bool {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) == 0
}
