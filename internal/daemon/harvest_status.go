package daemon

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HarvestAgentStat is one harness the portal shows on Connect.
type HarvestAgentStat struct {
	Name      string `json:"name"`
	Format    string `json:"format"`
	CwdMatch  bool   `json:"cwd_match,omitempty"`
	Dirs      int    `json:"dirs"`
	FilesSeen int    `json:"files_seen"`
	Active    bool   `json:"active"`
}

// HarvestLogLine is a recent harvester/processor event for the portal feed.
type HarvestLogLine struct {
	At     string `json:"at"`
	Type   string `json:"type"`
	Agent  string `json:"agent,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// HarvestStatus is the Connect-page snapshot of auto-sync / scanning.
type HarvestStatus struct {
	OK              bool               `json:"ok"`
	Running         bool               `json:"running"`
	Designated      bool               `json:"designated"`
	ProjectID       string             `json:"project_id,omitempty"`
	Root            string             `json:"root,omitempty"`
	PollSeconds     int                `json:"poll_seconds"`
	IdleSeconds     int                `json:"idle_seconds"`
	LastScanAt      string             `json:"last_scan_at,omitempty"`
	LastScanFiles   int                `json:"last_scan_files"`
	LastScanTurns   int                `json:"last_scan_turns"`
	LastScanError   string             `json:"last_scan_error,omitempty"`
	TrackedFiles    int                `json:"tracked_files"`
	ActiveSessions  int                `json:"active_sessions"`
	TurnsEmitted    int64              `json:"turns_emitted"`
	Completions     int64              `json:"completions"`
	ProposalsSaved  int64              `json:"proposals_saved"`
	ProposalErrors  int64              `json:"proposal_errors"`
	LastProposalErr string             `json:"last_proposal_error,omitempty"`
	LastEventAt     string             `json:"last_event_at,omitempty"`
	Agents          []HarvestAgentStat `json:"agents"`
	Files           []HarvestFileHit   `json:"files,omitempty"`
	Recent          []HarvestLogLine   `json:"recent"`
	Message         string             `json:"message,omitempty"`
}

// SetPipeline attaches the extraction Runtime so /local/harvest can report it.
func (d *Daemon) SetPipeline(r *Runtime) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.pipeline = r
	d.mu.Unlock()
}

// Pipeline returns the attached Runtime (nil when local-only / tests).
func (d *Daemon) Pipeline() *Runtime {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pipeline
}

func (d *Daemon) handleLocalHarvest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, d.HarvestSnapshot())
	case http.MethodPost:
		// POST /local/harvest → force one ScanAndTail + CheckIdle now.
		msg := d.TriggerHarvestScan()
		st := d.HarvestSnapshot()
		st.Message = msg
		writeJSON(w, http.StatusOK, st)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HarvestSnapshot builds the portal Connect harvest view.
func (d *Daemon) HarvestSnapshot() HarvestStatus {
	st := HarvestStatus{OK: true, Root: "", Agents: []HarvestAgentStat{}, Recent: []HarvestLogLine{}}
	if d == nil {
		st.Message = "daemon not configured"
		return st
	}
	st.Root = d.Root
	rt := d.Pipeline()
	if rt == nil {
		st.Message = "Harvest pipeline not started on this daemon."
		st.Agents = sourceAgentStats(nil)
		return st
	}
	return rt.HarvestSnapshot()
}

// TriggerHarvestScan forces an immediate transcript scan (Connect "Scan now").
func (d *Daemon) TriggerHarvestScan() string {
	rt := d.Pipeline()
	if rt == nil || rt.Harvester == nil {
		return "No harvester running"
	}
	nFiles, nTurns, err := rt.Harvester.ScanAndTailCounted()
	_ = rt.Harvester.CheckIdle()
	rt.noteScan(nFiles, nTurns, err)
	if err != nil {
		return "Scan finished with errors: " + err.Error()
	}
	return "Scan complete"
}

func sourceAgentStats(seen map[string]int) []HarvestAgentStat {
	byName := map[string]*HarvestAgentStat{}
	for _, src := range ResolveSources() {
		cur, ok := byName[src.Agent]
		if !ok {
			cur = &HarvestAgentStat{Name: src.Agent, Format: src.Format, CwdMatch: src.CwdMatch}
			byName[src.Agent] = cur
		}
		cur.Dirs += len(src.Dirs)
		if src.CwdMatch {
			cur.CwdMatch = true
		}
		if src.Format == FormatJSONL {
			cur.Format = FormatJSONL
		}
	}
	for name, n := range seen {
		if cur, ok := byName[name]; ok {
			cur.FilesSeen = n
			cur.Active = n > 0
		} else {
			byName[name] = &HarvestAgentStat{Name: name, FilesSeen: n, Active: n > 0, Format: "unknown"}
		}
	}
	out := make([]HarvestAgentStat, 0, len(byName))
	for _, v := range byName {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].Name < out[j].Name
	})
	return out
}

const maxHarvestLog = 40

func (r *Runtime) noteScan(files, turns int, err error) {
	if r == nil {
		return
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.lastScanAt = time.Now().UTC()
	r.lastScanFiles = files
	r.lastScanTurns = turns
	if err != nil {
		r.lastScanErr = err.Error()
	} else {
		r.lastScanErr = ""
	}
	detail := strconv.Itoa(files) + " files, " + strconv.Itoa(turns) + " new turns"
	r.pushLogLocked(HarvestLogLine{
		At:     r.lastScanAt.Format(time.RFC3339),
		Type:   "scan",
		Detail: detail,
	})
}

func (r *Runtime) noteHarvestEvent(ev Event) {
	if r == nil {
		return
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.lastEventAt = time.Now().UTC()
	agent, _ := ev.Payload["agent"].(string)
	detail := ""
	switch ev.Type {
	case EventConversationTurn:
		r.turnsEmitted++
		if s, ok := ev.Payload["content"].(string); ok {
			detail = trimDetail(s, 80)
		}
	case EventSessionComplete:
		r.completions++
		if s, ok := ev.Payload["detail"].(string); ok {
			detail = s
		}
	default:
		detail = string(ev.Type)
	}
	r.pushLogLocked(HarvestLogLine{
		At:     r.lastEventAt.Format(time.RFC3339),
		Type:   string(ev.Type),
		Agent:  agent,
		Detail: detail,
	})
}

func (r *Runtime) noteProposals(n int, err error) {
	if r == nil {
		return
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if n > 0 {
		r.proposalsSaved += int64(n)
		r.pushLogLocked(HarvestLogLine{
			At:     time.Now().UTC().Format(time.RFC3339),
			Type:   "proposal",
			Detail: strconv.Itoa(n) + " uploaded to portal",
		})
	}
	if err != nil {
		r.proposalErrors++
		r.lastProposalErr = err.Error()
		r.pushLogLocked(HarvestLogLine{
			At:     time.Now().UTC().Format(time.RFC3339),
			Type:   "proposal_error",
			Detail: trimDetail(err.Error(), 120),
		})
	}
}

func (r *Runtime) pushLogLocked(line HarvestLogLine) {
	r.recent = append(r.recent, line)
	if len(r.recent) > maxHarvestLog {
		r.recent = r.recent[len(r.recent)-maxHarvestLog:]
	}
}

// HarvestSnapshot assembles Connect telemetry from harvester + processor.
func (r *Runtime) HarvestSnapshot() HarvestStatus {
	st := HarvestStatus{OK: true, Running: true, Agents: []HarvestAgentStat{}, Recent: []HarvestLogLine{}}
	if r == nil {
		st.Running = false
		st.Message = "pipeline nil"
		return st
	}
	st.ProjectID = r.ProjectID
	if r.Processor != nil {
		st.Designated = r.Processor.Designated
	}
	if r.Daemon != nil {
		st.Root = r.Daemon.Root
	}
	seen := map[string]int{}
	if r.Harvester != nil {
		st.PollSeconds = int(r.Harvester.PollInterval / time.Second)
		st.IdleSeconds = int(r.Harvester.IdleTimeout / time.Second)
		hs := r.Harvester.Stats()
		st.LastScanAt = hs.LastScanAt
		st.LastScanFiles = hs.LastScanFiles
		st.LastScanTurns = hs.LastScanTurns
		st.LastScanError = hs.LastScanError
		st.TrackedFiles = hs.TrackedFiles
		st.ActiveSessions = hs.ActiveSessions
		seen = hs.AgentFiles
		st.Files = hs.RecentFiles
	}
	st.Agents = sourceAgentStats(seen)

	r.statsMu.Lock()
	st.TurnsEmitted = r.turnsEmitted
	st.Completions = r.completions
	st.ProposalsSaved = r.proposalsSaved
	st.ProposalErrors = r.proposalErrors
	st.LastProposalErr = r.lastProposalErr
	if !r.lastEventAt.IsZero() {
		st.LastEventAt = r.lastEventAt.Format(time.RFC3339)
	}
	st.Recent = append([]HarvestLogLine(nil), r.recent...)
	r.statsMu.Unlock()

	switch {
	case !st.Designated:
		st.Message = "Daemon online but not designated — proposals stay local until designation is on."
	case st.ProjectID == "" || !looksLikeUUID(st.ProjectID):
		st.Message = "Harvesting locally — waiting for server project id."
	case st.ProposalsSaved > 0:
		st.Message = "Auto-sync active — harvested chats upload as PROPOSED memories."
	default:
		st.Message = "Scanning agent transcripts — new turns sync to the portal review queue."
	}
	return st
}

func looksLikeUUID(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 32 && strings.Count(s, "-") >= 4
}

func trimDetail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
