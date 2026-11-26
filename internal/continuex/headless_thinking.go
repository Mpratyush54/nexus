package continuex

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// The agent's thinking, for the web relay: Claude Code's thinking blocks
// (stream-json thinking_delta, or whole thinking blocks in an assistant
// message) and Codex's reasoning items. Only one-shot turns (continue.here)
// read it here; live chats get thinking_delta events instead.

// MaxThinkingBytes bounds the thinking a progress update carries. Longer
// thinking keeps its latest part, after a leading "…".
const MaxThinkingBytes = 24 << 10

// thinkingReportEvery throttles thinking progress: the relay flushes every
// 350 ms anyway, so a faster rate only costs copies.
const thinkingReportEvery = 250 * time.Millisecond

// thinkingPiece is what one output line says about thinking.
type thinkingPiece struct {
	text  string
	id    string // a Codex reasoning item: its text replaces the item's earlier text
	delta bool   // appended to the current thinking block
	start bool   // a new thinking block starts
}

// streamThinking reads the thinking one JSONL line carries, if any.
func streamThinking(line string) (thinkingPiece, bool) {
	if !strings.Contains(line, "thinking") && !strings.Contains(line, "reasoning") {
		return thinkingPiece{}, false
	}
	var row struct {
		Type  string `json:"type"`
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"content_block"`
			Delta struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
		} `json:"event"`
		Message struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Item struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(line), &row) != nil {
		return thinkingPiece{}, false
	}
	switch row.Type {
	case "stream_event":
		if row.Event.Type == "content_block_start" && row.Event.ContentBlock.Type == "thinking" {
			return thinkingPiece{text: row.Event.ContentBlock.Thinking, delta: true, start: true}, true
		}
		if row.Event.Type == "content_block_delta" && row.Event.Delta.Type == "thinking_delta" {
			return thinkingPiece{text: row.Event.Delta.Thinking, delta: true}, true
		}
	case "assistant":
		var blocks []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		}
		if json.Unmarshal(row.Message.Content, &blocks) != nil {
			return thinkingPiece{}, false
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "thinking" && strings.TrimSpace(b.Thinking) != "" {
				parts = append(parts, b.Thinking)
			}
		}
		if len(parts) > 0 {
			return thinkingPiece{text: strings.Join(parts, "\n\n"), id: "claude:" + row.Message.ID}, true
		}
	case "item.started", "item.updated", "item.completed":
		if row.Item.Type != "reasoning" {
			return thinkingPiece{}, false
		}
		text := row.Item.Text
		if text == "" {
			var parts []string
			for _, s := range row.Item.Summary {
				if strings.TrimSpace(s.Text) != "" {
					parts = append(parts, s.Text)
				}
			}
			text = strings.Join(parts, "\n\n")
		}
		return thinkingPiece{text: text, id: "codex:" + row.Item.ID}, true
	}
	return thinkingPiece{}, false
}

// thinkingBuffer keeps a turn's thinking as blocks, bounded.
type thinkingBuffer struct {
	blocks   []string
	ids      []string
	sawDelta bool
	size     int
}

// add applies a piece; it reports whether the thinking text changed.
func (b *thinkingBuffer) add(p thinkingPiece) bool {
	switch {
	case p.delta:
		// Whole thinking blocks in Claude's assistant messages repeat what
		// the deltas already said.
		b.sawDelta = true
		if p.start || len(b.blocks) == 0 || b.ids[len(b.blocks)-1] != "" {
			if p.text == "" && len(b.blocks) > 0 && b.blocks[len(b.blocks)-1] == "" {
				return false
			}
			b.blocks = append(b.blocks, "")
			b.ids = append(b.ids, "")
		}
		if p.text == "" {
			return false
		}
		b.blocks[len(b.blocks)-1] += p.text
		b.size += len(p.text)
	case p.id != "":
		if strings.HasPrefix(p.id, "claude:") && b.sawDelta {
			return false
		}
		for i, id := range b.ids {
			if id == p.id {
				if b.blocks[i] == p.text {
					return false
				}
				b.size += len(p.text) - len(b.blocks[i])
				b.blocks[i] = p.text
				b.compact()
				return true
			}
		}
		if strings.TrimSpace(p.text) == "" {
			return false
		}
		b.blocks = append(b.blocks, p.text)
		b.ids = append(b.ids, p.id)
		b.size += len(p.text)
	default:
		return false
	}
	b.compact()
	return true
}

// compact drops the oldest blocks once far past the limit, so a very long
// turn never holds more than a few limits of thinking.
func (b *thinkingBuffer) compact() {
	for b.size > 4*MaxThinkingBytes && len(b.blocks) > 1 {
		b.size -= len(b.blocks[0])
		b.blocks, b.ids = b.blocks[1:], b.ids[1:]
	}
	if last := len(b.blocks) - 1; last >= 0 && len(b.blocks[last]) > 4*MaxThinkingBytes {
		cut := len(b.blocks[last]) - 2*MaxThinkingBytes
		b.size -= cut
		b.blocks[last] = b.blocks[last][cut:]
	}
}

// String is the thinking so far, capped to MaxThinkingBytes.
func (b *thinkingBuffer) String() string {
	parts := make([]string, 0, len(b.blocks))
	for _, block := range b.blocks {
		if t := strings.TrimSpace(block); t != "" {
			parts = append(parts, t)
		}
	}
	return CapThinking(strings.Join(parts, "\n\n"))
}

// CapThinking keeps the latest MaxThinkingBytes of thinking, on a rune
// boundary, after a leading "…".
func CapThinking(text string) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= MaxThinkingBytes {
		return text
	}
	cut := len(text) - (MaxThinkingBytes - len("…"))
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return "…" + text[cut:]
}
