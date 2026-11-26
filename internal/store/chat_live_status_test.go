package store

import (
	"testing"
	"time"
)

func TestChatLiveReportMerge(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	row, read := applyChatLiveReport(chatLiveRow{}, false, ChatLiveReport{SessionID: "a", Status: ChatLiveRunning}, t0)
	if read || row.Status != ChatLiveRunning || row.Unread || !row.StatusAt.Equal(t0) {
		t.Fatalf("new row %+v", row)
	}
	// Opened on the web while it ran: the reply that follows is still new.
	readAt := t0.Add(time.Second)
	row.ReadAt = &readAt
	row, read = applyChatLiveReport(row, true, ChatLiveReport{SessionID: "a", Status: ChatLiveDone, Unread: true}, t0.Add(5*time.Second))
	if read || !row.Unread || !row.StatusAt.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("done row %+v read=%v", row, read)
	}
	// Read on the web after it finished: the desktop's repeat stays read and learns it.
	readAt = t0.Add(10 * time.Second)
	row.ReadAt, row.Unread = &readAt, false
	row, read = applyChatLiveReport(row, true, ChatLiveReport{SessionID: "a", Status: ChatLiveDone, Unread: true}, t0.Add(60*time.Second))
	if !read || row.Unread {
		t.Fatalf("read row %+v read=%v", row, read)
	}
	if !keepChatLiveRow(row) {
		t.Fatal("a done row is kept until it goes idle")
	}
	if keepChatLiveRow(chatLiveRow{Status: ChatLiveIdle}) {
		t.Fatal("an idle, read row is dropped")
	}
}

func TestChatLiveShownAndStale(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	running := chatLiveRow{Status: ChatLiveRunning, UpdatedAt: t0}
	if st, ok := shownChatLive(running, t0.Add(time.Minute)); !ok || st.Status != ChatLiveRunning {
		t.Fatalf("fresh running %+v %v", st, ok)
	}
	if _, ok := shownChatLive(running, t0.Add(ChatLiveStale+time.Second)); ok {
		t.Fatal("a desktop that went quiet still shows running")
	}
	if st, ok := shownChatLive(chatLiveRow{Status: ChatLiveIdle, Unread: true, UpdatedAt: t0}, t0); !ok || st.Status != ChatLiveDone {
		t.Fatalf("idle unread %+v %v", st, ok)
	}
	if _, ok := shownChatLive(chatLiveRow{Status: ChatLiveDone, UpdatedAt: t0}, t0); !ok {
		t.Fatal("done is listed (the web decides from unread)")
	}
}

func TestMemChatLiveStatusRoundTrip(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	if _, err := m.PutChatLiveStatus(ctx, "alice", []ChatLiveReport{{SessionID: "bad id", Status: ChatLiveRunning}}); err != ErrBadChatLiveReport {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := m.PutChatLiveStatus(ctx, "alice", []ChatLiveReport{{SessionID: "a", Status: "busy"}}); err != ErrBadChatLiveReport {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := m.PutChatLiveStatus(ctx, "alice", []ChatLiveReport{
		{SessionID: "a", Status: ChatLiveRunning}, {SessionID: "b", Status: ChatLiveDone, Unread: true}, {SessionID: "c", Status: ChatLiveIdle},
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := m.ListChatLiveStatus(ctx, "alice")
	if len(list) != 2 || list[0].SessionID != "a" || list[1].SessionID != "b" || !list[1].Unread {
		t.Fatalf("list %+v", list)
	}
	if other, _ := m.ListChatLiveStatus(ctx, "bob"); len(other) != 0 {
		t.Fatalf("bob sees alice's chats: %+v", other)
	}
	time.Sleep(2 * time.Millisecond)
	if err := m.MarkChatRead(ctx, "alice", "b"); err != nil {
		t.Fatal(err)
	}
	read, _ := m.PutChatLiveStatus(ctx, "alice", []ChatLiveReport{{SessionID: "b", Status: ChatLiveDone, Unread: true}})
	if len(read) != 1 || read[0] != "b" {
		t.Fatalf("read = %v", read)
	}
	_, _ = m.PutChatLiveStatus(ctx, "alice", []ChatLiveReport{{SessionID: "b", Status: ChatLiveIdle}, {SessionID: "a", Status: ChatLiveIdle}})
	if list, _ := m.ListChatLiveStatus(ctx, "alice"); len(list) != 0 {
		t.Fatalf("idle chats still listed: %+v", list)
	}
}
