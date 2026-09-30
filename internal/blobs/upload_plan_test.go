package blobs

import "testing"

func TestPlanUpload(t *testing.T) {
	sum := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	one, err := PlanUpload(sum, 100)
	if err != nil || one.Mode != "single" || one.Parts != 1 || one.ChecksumB64 == "" {
		t.Fatalf("single: %+v %v", one, err)
	}
	multi, err := PlanUpload(sum, ChunkThreshold+1)
	if err != nil || multi.Mode != "multipart" || multi.PartSize != DefaultChunkSize || multi.Parts < 2 {
		t.Fatalf("multi: %+v %v", multi, err)
	}
	if _, err := PlanUpload(sum, MaxFileBytes+1); err != ErrTooLarge {
		t.Fatalf("huge: %v", err)
	}
}
