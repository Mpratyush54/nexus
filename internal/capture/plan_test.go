package capture

import "testing"

func TestPlanTreeSkipsRegenerableAndRefusesHuge(t *testing.T) {
	files := []FileEntry{
		{Rel: "package.json", Size: 20},
		{Rel: "src/app.ts", Size: 40},
		{Rel: "node_modules/leftpad/index.js", Size: 10},
		{Rel: "config.local.json", Size: 8},
		{Rel: "video.bin", Size: (10 << 30) + 1},
	}
	m := MarkersFromNames([]string{"package.json"})
	plan := PlanTree(files, m)
	if len(plan.Include) != 3 {
		t.Fatalf("include = %v", plan.Include)
	}
	if len(plan.Exclude) != 1 || plan.Exclude[0] != "node_modules/leftpad/index.js" {
		t.Fatalf("exclude = %v", plan.Exclude)
	}
	if len(plan.Rebuild) != 1 || plan.Rebuild[0] != "default:node" {
		t.Fatalf("rebuild = %v", plan.Rebuild)
	}
	if len(plan.Refuse) != 1 || plan.Refuse[0] != "video.bin" {
		t.Fatalf("refuse = %v", plan.Refuse)
	}
}
