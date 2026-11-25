package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"central-memory/internal/blobs"
	"central-memory/internal/capture"
)

const (
	teleportMaxFile  = 16 << 20
	teleportMaxTotal = 128 << 20
	teleportMaxFiles = 2000
)

type bundleSession struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	WorkspaceRootHint string `json:"workspace_root_hint"`
	Harness           string `json:"harness"`
	NativeID          string `json:"native_id"`
}

type bundleVersion struct {
	Version  int             `json:"version"`
	State    string          `json:"state"`
	Manifest json.RawMessage `json:"manifest"`
}

type bundleManifest struct {
	Harness    string `json:"harness,omitempty"`
	NativeID   string `json:"native_id,omitempty"`
	Transcript struct {
		Blob string `json:"blob"`
	} `json:"transcript"`
	Files []struct {
		Path string `json:"path"`
		Blob string `json:"blob"`
		Size int64  `json:"size"`
	} `json:"files"`
	Exclude []string                `json:"exclude,omitempty"`
	Rebuild []capture.RebuildRecipe `json:"rebuild,omitempty"`
}

func cloudDecode(ctx context.Context, cloud Cloud, method, path string, request any, out any) error {
	var body []byte
	if request != nil {
		var err error
		body, err = json.Marshal(request)
		if err != nil {
			return err
		}
	}
	raw, err := cloud.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func loadBundleContext(ctx context.Context, cloud Cloud, sessionID string, requireFiles bool) (bundleSession, bundleVersion, bundleManifest, error) {
	var session bundleSession
	base := "/v1/agent-sessions/" + url.PathEscape(sessionID)
	if err := cloudDecode(ctx, cloud, http.MethodGet, base, nil, &session); err != nil {
		return session, bundleVersion{}, bundleManifest{}, err
	}
	var list struct {
		Items []bundleVersion `json:"items"`
	}
	if err := cloudDecode(ctx, cloud, http.MethodGet, base+"/versions", nil, &list); err != nil {
		return session, bundleVersion{}, bundleManifest{}, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Version > list.Items[j].Version })
	for _, version := range list.Items {
		if version.State != "complete" && version.State != "transcript_only" {
			continue
		}
		var manifest bundleManifest
		if json.Unmarshal(version.Manifest, &manifest) != nil {
			continue
		}
		if manifest.Transcript.Blob != "" && (!requireFiles || len(manifest.Files) > 0) {
			return session, version, manifest, nil
		}
	}
	if requireFiles {
		return session, bundleVersion{}, bundleManifest{}, errors.New("this transfer contains a transcript only; no code was uploaded")
	}
	return session, bundleVersion{}, bundleManifest{}, errors.New("this session has no completed transcript to transfer")
}

// teleportSendWithCode captures the CURRENT local workspace and publishes an
// immutable version with code blobs before creating the teleport. It never
// silently substitutes a transcript-only transfer when capture fails.
func teleportSendWithCode(ctx context.Context, deps Deps, args json.RawMessage) (any, error) {
	if deps.Cloud == nil {
		return nil, ErrOffline
	}
	var req struct {
		SessionID     string `json:"session_id"`
		ToUserID      string `json:"to_user_id"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := decode(args, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.ToUserID) == "" {
		return nil, errors.New("select a session and recipient")
	}
	session, _, previous, err := loadBundleContext(ctx, deps.Cloud, req.SessionID, false)
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root == "" {
		root = strings.TrimSpace(deps.Root)
	}
	if root == "" || strings.TrimSpace(session.WorkspaceRootHint) == "" {
		return nil, errors.New("the original local workspace is required to send code")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absHint, err := filepath.Abs(session.WorkspaceRootHint)
	if err != nil || !sameWorkspacePath(absRoot, absHint) {
		return nil, errors.New("the selected folder does not match this session's original workspace")
	}
	stat, err := os.Stat(absRoot)
	if err != nil || !stat.IsDir() {
		return nil, errors.New("this session's workspace is not available on this machine")
	}
	local := blobs.NewByteaStore()
	manifest, err := capture.CaptureWalkLimited(absRoot, local, teleportMaxFile, teleportMaxTotal)
	if err != nil {
		return nil, fmt.Errorf("code capture failed: %w", err)
	}
	if len(manifest.Refuse) > 0 {
		return nil, fmt.Errorf("code package exceeds the safe transfer limit; first refused file: %s", manifest.Refuse[0])
	}
	if len(manifest.Files) == 0 {
		return nil, errors.New("no shareable code files were found in this workspace")
	}
	if len(manifest.Files) > teleportMaxFiles {
		return nil, fmt.Errorf("code package has %d files; limit is %d", len(manifest.Files), teleportMaxFiles)
	}
	hashes := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		hashes = append(hashes, file.SHA256)
	}
	var missing struct {
		Missing []string `json:"missing"`
	}
	if err := cloudDecode(ctx, deps.Cloud, http.MethodPost, "/v1/blobs/missing", map[string]any{"project_id": session.ProjectID, "hashes": hashes}, &missing); err != nil {
		return nil, err
	}
	for _, hash := range missing.Missing {
		body, err := local.Get(hash)
		if err != nil {
			return nil, err
		}
		if err := uploadTeleportBlob(ctx, deps.Cloud, session.ProjectID, hash, body); err != nil {
			return nil, fmt.Errorf("upload code blob %s: %w", hash[:12], err)
		}
	}
	base := "/v1/agent-sessions/" + url.PathEscape(req.SessionID)
	var created bundleVersion
	if err := cloudDecode(ctx, deps.Cloud, http.MethodPost, base+"/versions", map[string]any{}, &created); err != nil {
		return nil, err
	}
	if created.Version < 1 {
		return nil, errors.New("server did not create a session version")
	}
	complete := bundleManifest{Harness: session.Harness, NativeID: session.NativeID, Transcript: previous.Transcript, Exclude: manifest.Exclude, Rebuild: manifest.Rebuild}
	for _, file := range manifest.Files {
		complete.Files = append(complete.Files, struct {
			Path string `json:"path"`
			Blob string `json:"blob"`
			Size int64  `json:"size"`
		}{Path: file.Path, Blob: "sha256:" + file.SHA256, Size: file.Size})
	}
	if err := cloudDecode(ctx, deps.Cloud, http.MethodPost, base+"/versions/"+fmt.Sprint(created.Version)+"/complete", complete, nil); err != nil {
		return nil, fmt.Errorf("could not finalize code package: %w", err)
	}
	var sent map[string]any
	if err := cloudDecode(ctx, deps.Cloud, http.MethodPost, "/v1/teleports", map[string]string{"session_id": req.SessionID, "to_user_id": req.ToUserID}, &sent); err != nil {
		return nil, err
	}
	sent["code_files"] = len(manifest.Files)
	sent["excluded_paths"] = len(manifest.Exclude)
	sent["version"] = created.Version
	return sent, nil
}

func uploadTeleportBlob(ctx context.Context, cloud Cloud, projectID, hash string, body []byte) error {
	if len(body) <= 1<<20 {
		return cloudDecode(ctx, cloud, http.MethodPut, "/v1/blobs/"+hash, map[string]any{"project_id": projectID, "body_b64": base64.StdEncoding.EncodeToString(body), "kind": "plain", "purpose": "file"}, nil)
	}
	chunks, err := blobs.Split(body, 1<<20)
	if err != nil {
		return err
	}
	path := "/v1/blobs/" + hash
	for _, chunk := range chunks {
		if err := cloudDecode(ctx, cloud, http.MethodPost, path+"/chunks", map[string]any{"project_id": projectID, "index": chunk.Index, "total": chunk.Total, "chunk_sha256": chunk.SHA256, "body_b64": base64.StdEncoding.EncodeToString(chunk.Body)}, nil); err != nil {
			return err
		}
	}
	return cloudDecode(ctx, cloud, http.MethodPost, path+"/assemble", map[string]string{"project_id": projectID}, nil)
}

// teleportRestoreCode downloads a version into a NEW directory. No existing
// checkout is overwritten and no dependency/rebuild commands are executed.
func teleportRestoreCode(ctx context.Context, deps Deps, args json.RawMessage) (any, error) {
	if deps.Cloud == nil {
		return nil, ErrOffline
	}
	var req struct {
		SessionID string `json:"session_id"`
		Dest      string `json:"dest"`
	}
	if err := decode(args, &req); err != nil {
		return nil, err
	}
	if req.SessionID == "" || strings.TrimSpace(req.Dest) == "" {
		return nil, errors.New("session and destination are required")
	}
	_, version, manifest, err := loadBundleContext(ctx, deps.Cloud, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	if len(manifest.Files) == 0 {
		return nil, errors.New("this transfer contains a transcript only; no code was uploaded")
	}
	if len(manifest.Files) > teleportMaxFiles {
		return nil, errors.New("code manifest has too many files")
	}
	var total int64
	type restored struct {
		path string
		body []byte
	}
	files := make([]restored, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		if !filepath.IsLocal(file.Path) || strings.Contains(file.Path, "\\") {
			return nil, errors.New("unsafe code path in transfer")
		}
		if file.Size < 0 || file.Size > teleportMaxFile || total > teleportMaxTotal-file.Size {
			return nil, errors.New("code package exceeds restore limits")
		}
		total += file.Size
		hash := blobs.NormalizeSHA256(file.Blob)
		if len(hash) != 64 {
			return nil, errors.New("invalid code blob reference")
		}
		path := "/v1/agent-sessions/" + url.PathEscape(req.SessionID) + "/versions/" + fmt.Sprint(version.Version) + "/blobs/" + hash
		body, err := deps.Cloud.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != file.Size || blobs.HashBytes(body) != hash {
			return nil, fmt.Errorf("code hash mismatch: %s", file.Path)
		}
		files = append(files, restored{path: file.Path, body: body})
	}
	dest, err := filepath.Abs(req.Dest)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, errors.New("choose a new destination; Nexus will not overwrite an existing folder")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		return nil, err
	}
	for _, file := range files {
		out := filepath.Join(dest, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return nil, fmt.Errorf("restore incomplete in %s: %w", dest, err)
		}
		if err := os.WriteFile(out, file.body, 0o600); err != nil {
			return nil, fmt.Errorf("restore incomplete in %s: %w", dest, err)
		}
	}
	return map[string]any{"dest": dest, "verified": len(files), "excluded_paths": len(manifest.Exclude), "note": "Known credential paths and regenerable dependencies were excluded; no commands were run."}, nil
}
