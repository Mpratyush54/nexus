package localclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetStatusAndHarvest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/local/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Status{Connected: true, Username: "ada", Root: `D:\proj`, Message: "ok"})
	})
	mux.HandleFunc("/local/harvest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Harvest{
			ActiveSessions: 2,
			Agents:         []HarvestAgent{{Name: "cursor", FilesSeen: 3}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	st, err := c.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Username != "ada" {
		t.Fatalf("%+v", st)
	}
	h, err := c.GetHarvest()
	if err != nil {
		t.Fatal(err)
	}
	if h.ActiveSessions != 2 || len(h.Agents) != 1 {
		t.Fatalf("%+v", h)
	}
	if !c.Online() {
		t.Fatal("expected online")
	}
}
