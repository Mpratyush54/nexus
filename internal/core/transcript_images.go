package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"central-memory/internal/continuex"
	"central-memory/internal/transcriptimages"
)

// transcriptImageCache is where decoded transcript images are written for
// the chat view. Local only: nothing in it is uploaded.
var transcriptImageCache = func() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Nexus", "transcript-images"), nil
}

const transcriptImageCacheFiles = 300

// transcriptImagesCall serves the desktop chat view:
//
//	transcript.images {agent, native_id}                -> {items: [Ref]}
//	transcript.image  {agent, native_id, offset, index} -> {path, media_type, bytes}
//
// Both read the agent's transcript on this computer only. A chat captured on
// another computer simply has no local images.
func transcriptImagesCall(name string, args json.RawMessage) (any, error) {
	var req struct {
		Agent    string `json:"agent"`
		NativeID string `json:"native_id"`
		Offset   int64  `json:"offset"`
		Index    int    `json:"index"`
	}
	if err := decode(args, &req); err != nil {
		return nil, err
	}
	path := transcriptimages.Find(req.Agent, strings.TrimSpace(req.NativeID))
	if name == "transcript.images" {
		if path == "" {
			return map[string]any{"items": []transcriptimages.Ref{}, "local": false}, nil
		}
		refs, err := transcriptimages.List(path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": refs, "local": true}, nil
	}
	if path == "" {
		return nil, errors.New("this chat's transcript is not on this computer")
	}
	dir, err := transcriptImageCache()
	if err != nil {
		return nil, err
	}
	// The cache name is derived from the reference, so opening an image
	// twice reuses the file instead of decoding it again.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", path, req.Offset, req.Index)))
	key := hex.EncodeToString(sum[:16])
	if matches, _ := filepath.Glob(filepath.Join(dir, key+".*")); len(matches) == 1 {
		if info, statErr := os.Stat(matches[0]); statErr == nil && info.Size() > 0 {
			return map[string]any{"path": matches[0], "media_type": mediaTypeForExt(filepath.Ext(matches[0])), "bytes": info.Size()}, nil
		}
	}
	mediaType, data, err := transcriptimages.Extract(path, req.Offset, req.Index)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	pruneImageCache(dir, transcriptImageCacheFiles)
	target := filepath.Join(dir, key+continuex.ImageExtension(mediaType))
	tmp, err := os.CreateTemp(dir, ".img-*")
	if err != nil {
		return nil, err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return nil, errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return map[string]any{"path": target, "media_type": mediaType, "bytes": len(data)}, nil
}

func mediaTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// pruneImageCache keeps the newest files so the cache cannot grow forever.
func pruneImageCache(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) < keep {
		return
	}
	type file struct {
		path string
		at   int64
	}
	files := []file{}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			files = append(files, file{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at > files[j].at })
	for _, f := range files[min(len(files), keep-1):] {
		_ = os.Remove(f.path)
	}
}
