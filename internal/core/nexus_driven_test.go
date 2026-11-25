package core

import (
	"testing"
)

func TestTimelineHidesCapturedCopiesOfNexusChats(t *testing.T) {
	root := t.TempDir()
	// "nexus-chat" ran Claude session N1 inside Nexus; "resumed" continued its
	// own captured session N2.
	for _, ex := range []ContinuationExchange{
		{SessionID: "nexus-chat", Agent: "claude", NativeID: "N1", Prompt: "p", Answer: "a"},
		{SessionID: "resumed", Agent: "claude", NativeID: "N2", Prompt: "p", Answer: "a"},
	} {
		if _, err := saveContinuation(root, ex); err != nil {
			t.Fatal(err)
		}
	}
	out := hideNexusDrivenCopies(map[string]any{"items": []any{
		map[string]any{"session_id": "nexus-chat", "native_id": "nexus-native"},
		map[string]any{"session_id": "captured-copy", "native_id": "N1"},
		map[string]any{"session_id": "resumed", "native_id": "N2"},
		map[string]any{"session_id": "other", "native_id": "N9"},
	}}, root).(map[string]any)
	var got []string
	for _, it := range out["items"].([]any) {
		got = append(got, it.(map[string]any)["session_id"].(string))
	}
	want := []string{"nexus-chat", "resumed", "other"}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
}
