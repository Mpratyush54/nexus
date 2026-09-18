package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/store"
)

func runVersion(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("nexus version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "compare against the latest published CLI release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "nexus %s", buildinfo.Version)
	if buildinfo.Commit != "" && buildinfo.Commit != "unknown" {
		fmt.Fprintf(stdout, " (%s)", shortSHA(buildinfo.Commit))
	}
	fmt.Fprintln(stdout)
	if !*check {
		return nil
	}
	rel, err := fetchLatestCLI(ctx, cfg, "stable")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "latest stable: %s\n", rel.Version)
	if rel.Version == strings.TrimPrefix(buildinfo.Version, "v") || rel.Version == buildinfo.Version {
		fmt.Fprintln(stdout, "up to date")
	} else {
		fmt.Fprintln(stdout, "run: nexus update")
	}
	return nil
}

func runUpdate(_ context.Context, cfg Config, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("nexus update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	channel := fs.String("channel", "stable", "release channel")
	yes := fs.Bool("yes", false, "download the matching artifact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rel, err := fetchLatestCLI(ctx, cfg, *channel)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "latest %s CLI: %s\n", rel.Channel, rel.Version)
	art := matchArtifact(rel.Artifacts, runtime.GOOS, runtime.GOARCH)
	if art == nil {
		return fmt.Errorf("no %s/%s artifact in %s", runtime.GOOS, runtime.GOARCH, rel.Version)
	}
	fmt.Fprintf(stdout, "artifact: %s\n", art.URL)
	if !*yes {
		fmt.Fprintln(stdout, "re-run with --yes to download")
		return nil
	}
	path, err := downloadArtifact(ctx, art)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "downloaded %s\n", path)
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stdout, "replace the running nexus.exe with this file after exit")
	}
	return nil
}

func fetchLatestCLI(ctx context.Context, cfg Config, channel string) (*store.AppRelease, error) {
	c := newAPIClient(cfg)
	var rel store.AppRelease
	q := url.Values{}
	q.Set("app", buildinfo.AppCLI)
	q.Set("channel", channel)
	if err := c.getJSON(ctx, "/platform/releases/latest", q, &rel); err != nil {
		return nil, fmt.Errorf("lookup latest CLI release: %w", err)
	}
	return &rel, nil
}

func matchArtifact(arts []store.ReleaseArtifact, goos, goarch string) *store.ReleaseArtifact {
	for i := range arts {
		if strings.EqualFold(arts[i].OS, goos) && strings.EqualFold(arts[i].Arch, goarch) {
			return &arts[i]
		}
	}
	return nil
}

func downloadArtifact(ctx context.Context, art *store.ReleaseArtifact) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, art.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}
	name := strings.TrimSpace(art.Filename)
	if name == "" {
		name = filepath.Base(art.URL)
	}
	if name == "" || name == "." || name == "/" {
		name = "nexus-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "nexus", "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hasher), resp.Body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if want := strings.TrimSpace(art.SHA256); want != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, want) {
			return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, want)
		}
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o755)
	}
	return path, nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
