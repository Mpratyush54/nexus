package server

// What the person's desktop agents are doing in each chat, for the web
// chat list's status dots (the desktop sidebar's, spec D1).
//
//	PUT  /v1/chat-status            {items: [{session_id, status, unread}]} -> {read: [session_id]}
//	  Nexus Desktop reports chats whose status changed (debounced), and
//	  repeats working and unread ones every minute. read lists the chats it
//	  still calls unread that the person has opened on the web since: the
//	  desktop clears its own mark for them.
//	GET  /v1/chat-status            -> {items: [{session_id, status, unread, updated_at}]}
//	  Working chats (running, approval, queued; dropped after 3 minutes
//	  without a report) and unread ones (done, error), plus interrupted.
//	POST /v1/chat-status/{id}/read  -> {ok: true}
//	  The person opened the chat on the web.
//
// Statuses are the caller's own: they describe that person's desktop and
// are never shown to anyone else, so a report needs no chat access check.

import (
	"errors"
	"net/http"

	"central-memory/internal/store"
)

func (s *Server) registerChatStatusRoutes() {
	s.Mux.HandleFunc("GET /v1/chat-status", s.requireAuth(s.handleChatStatusList))
	s.Mux.HandleFunc("PUT /v1/chat-status", s.requireAuth(s.handleChatStatusPut))
	s.Mux.HandleFunc("POST /v1/chat-status/{id}/read", s.requireAuth(s.handleChatStatusRead))
}

func (s *Server) chatLiveStore(w http.ResponseWriter) (store.ChatLiveStatusStore, bool) {
	st, ok := s.Store.(store.ChatLiveStatusStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "chat status is unavailable")
	}
	return st, ok
}

func (s *Server) handleChatStatusList(w http.ResponseWriter, r *http.Request) {
	st, ok := s.chatLiveStore(w)
	if !ok {
		return
	}
	items, err := st.ListChatLiveStatus(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load chat status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleChatStatusPut(w http.ResponseWriter, r *http.Request) {
	st, ok := s.chatLiveStore(w)
	if !ok {
		return
	}
	var body struct {
		Items []store.ChatLiveReport `json:"items"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	read, err := st.PutChatLiveStatus(r.Context(), authSubject(r), body.Items)
	if errors.Is(err, store.ErrBadChatLiveReport) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save chat status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"read": read})
}

func (s *Server) handleChatStatusRead(w http.ResponseWriter, r *http.Request) {
	st, ok := s.chatLiveStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !store.ValidChatLiveReport(store.ChatLiveReport{SessionID: id, Status: store.ChatLiveIdle}) {
		writeError(w, http.StatusBadRequest, "a chat id is required")
		return
	}
	if err := st.MarkChatRead(r.Context(), authSubject(r), id); err != nil {
		writeError(w, http.StatusInternalServerError, "could not mark the chat read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
