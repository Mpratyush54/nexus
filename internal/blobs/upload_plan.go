package blobs

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// UploadPlan is the presigned-upload shape for one blob (spec 7.7).
type UploadPlan struct {
	SHA256      string
	Mode        string
	PartSize    int64
	Parts       int
	ChecksumB64 string
}

// PlanUpload chooses a single PUT or a 16 MiB multipart upload.
// Files over MaxFileBytes are refused. The checksum is the base64 of the
// raw SHA-256 bytes, which is what x-amz-checksum-sha256 carries.
func PlanUpload(sha string, size int64) (UploadPlan, error) {
	sha = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(sha)), "sha256:")
	raw, err := hex.DecodeString(sha)
	if err != nil || len(raw) != 32 {
		return UploadPlan{}, ErrCorrupt
	}
	if size < 0 || size > MaxFileBytes {
		return UploadPlan{}, ErrTooLarge
	}
	plan := UploadPlan{
		SHA256:      sha,
		Mode:        "single",
		PartSize:    size,
		Parts:       1,
		ChecksumB64: base64.StdEncoding.EncodeToString(raw),
	}
	if size > ChunkThreshold {
		plan.Mode = "multipart"
		plan.PartSize = DefaultChunkSize
		plan.Parts = int((size + DefaultChunkSize - 1) / DefaultChunkSize)
	}
	return plan, nil
}
