package continuex

import "testing"

func TestCommand(t *testing.T) {
	here, err := Command(Request{SessionID: "s1", Mode: "here", Resume: "fork", Prompt: "go"})
	if err != nil || here[0] != "nexus" || here[3] != "--session" {
		t.Fatalf("here: %v %v", here, err)
	}
	open, err := Command(Request{SessionID: "s1", Mode: "open_in_agent", Agent: "cursor"})
	if err != nil || open[3] != "cursor" {
		t.Fatalf("open: %v %v", open, err)
	}
	if _, err := Command(Request{SessionID: "s1", Mode: "nope"}); err == nil {
		t.Fatal("bad mode")
	}
}
