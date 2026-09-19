package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalHarvestEndpoint(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "tok-harvest-ui")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(d, "central-memory", NewStaticDesignation(true))
	d.SetPipeline(rt)

	// GET without scan yet — still lists harnesses.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/local/harvest", nil)
	d.handleLocalHarvest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", rec.Code, rec.Body.String())
	}
	var st HarvestStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.OK || !st.Running || !st.Designated {
		t.Fatalf("snapshot = %+v", st)
	}
	if len(st.Agents) < 5 {
		t.Fatalf("expected multi-harness agents, got %d", len(st.Agents))
	}

	// POST forces a scan.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/local/harvest", nil)
	d.handleLocalHarvest(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST status = %d", rec2.Code)
	}
	var st2 HarvestStatus
	if err := json.Unmarshal(rec2.Body.Bytes(), &st2); err != nil {
		t.Fatal(err)
	}
	if st2.Message == "" {
		t.Fatal("expected scan message")
	}
}
