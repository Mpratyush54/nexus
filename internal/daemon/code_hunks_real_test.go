package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealTranscriptHunks is a read-only probe of a real transcript:
//
//	NEXUS_REAL_TRANSCRIPT=<jsonl> NEXUS_REAL_ROOT=<workspace> go test -run TestRealTranscriptHunks ./internal/daemon
//
// It parses the transcript as the harvester does and reports the edited
// files and the located hunks. Nothing is written or uploaded.
func TestRealTranscriptHunks(t *testing.T) {
	path, root := os.Getenv("NEXUS_REAL_TRANSCRIPT"), os.Getenv("NEXUS_REAL_ROOT")
	if path == "" || root == "" {
		t.Skip("set NEXUS_REAL_TRANSCRIPT and NEXUS_REAL_ROOT")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	var batch []Event
	calls, editCalls := 0, 0
	for sc.Scan() {
		turn, ok := parseTurnLine(sc.Bytes())
		if !ok {
			continue
		}
		p := turnPayload("append", "claude", path, turn)
		for _, c := range toolCallSlice(p["tool_calls"]) {
			calls++
			if name, _ := callToolName(c); editToolNames[name] {
				editCalls++
			}
		}
		batch = append(batch, Event{Type: EventConversationTurn, Payload: p, CreatedAt: time.Now()})
	}
	touched := TouchedPathsFromEvents(batch, root)
	edits := EditsFromEvents(batch, root)
	changes, _ := locateEdits(root, edits)
	raw, _ := json.Marshal(changes)
	t.Logf("turns=%d tool_calls=%d edit_calls=%d touched=%d edits=%d hunks=%d changes_json=%dB", len(batch), calls, editCalls, len(touched), len(edits), len(changes), len(raw))
	cands := codeBackfillCandidates(NewHarvesterWithPoll(root, nil, time.Second, time.Second), time.Now())
	owned := false
	for _, c := range cands {
		owned = owned || c.path == path
	}
	t.Logf("backfill candidates=%d includes this transcript=%v", len(cands), owned)
	if len(touched) > 0 {
		t.Logf("touched sample: %s", strings.Join(touched[:min(5, len(touched))], ", "))
	}
	for i, c := range changes {
		if i >= 3 {
			break
		}
		t.Logf("hunk %s:%d-%d +%d -%d hashes=%d", c.Path, c.StartLine, c.EndLine, c.AddedLines, c.RemovedLines, len(c.AddedHashes))
	}
}
