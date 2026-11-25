package core

import (
	"context"
	"encoding/json"
	"testing"
)

type captureConfigRecorder struct {
	calls int
	roots []string
}

func (*captureConfigRecorder) Pause() error  { return nil }
func (*captureConfigRecorder) Resume() error { return nil }
func (*captureConfigRecorder) Status() any   { return map[string]any{} }
func (c *captureConfigRecorder) ConfigureWorkspaces(roots []string) (any, error) {
	c.calls++
	c.roots = append([]string{}, roots...)
	return map[string]any{"approved_workspaces": roots}, nil
}

func TestCaptureConfigurationRequiresConsentAndSupportsRevocation(t *testing.T) {
	controller := &captureConfigRecorder{}
	deps := Deps{Capture: controller}
	if _, err := Call(context.Background(), "capture.configure", json.RawMessage(`{"workspaces":["folder"]}`), deps); err == nil || controller.calls != 0 {
		t.Fatal("capture scope changed without confirmation")
	}
	if _, err := Call(context.Background(), "capture.configure", json.RawMessage(`{"workspaces":["one","two"],"confirm":true}`), deps); err != nil {
		t.Fatal(err)
	}
	if len(controller.roots) != 2 {
		t.Fatalf("scope = %v", controller.roots)
	}
	if _, err := Call(context.Background(), "capture.configure", json.RawMessage(`{"workspaces":[],"confirm":true}`), deps); err != nil {
		t.Fatal(err)
	}
	if len(controller.roots) != 0 {
		t.Fatal("empty selection did not revoke capture")
	}
}

func TestDashboardAndCapturePolicyContract(t *testing.T) {
	cloud := &routeCloud{}
	deps := Deps{Cloud: cloud, Online: true}
	for _, method := range []string{"dashboard.get", "projects.capture_get"} {
		if _, err := Call(context.Background(), method, json.RawMessage(`{"project_id":"p/1"}`), deps); err != nil {
			t.Fatal(err)
		}
		want := "/projects/p%2F1/dashboard"
		if method == "projects.capture_get" {
			want = "/projects/p%2F1/capture"
		}
		if cloud.method != "GET" || cloud.path != want || len(cloud.body) != 0 {
			t.Fatalf("%s: %s %s %s", method, cloud.method, cloud.path, cloud.body)
		}
	}
	if _, err := Call(context.Background(), "projects.capture_set", json.RawMessage(`{"project_id":"p/1","enabled":false}`), deps); err != nil {
		t.Fatal(err)
	}
	if cloud.method != "PUT" || cloud.path != "/projects/p%2F1/capture" || string(cloud.body) != `{"enabled":false}` {
		t.Fatalf("policy request = %s %s %s", cloud.method, cloud.path, cloud.body)
	}
}
