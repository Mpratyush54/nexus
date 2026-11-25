package continuex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

type modelProbe struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	encoder   *json.Encoder
	scanner   *bufio.Scanner
	stopWatch func() bool
}

func startModelProbe(ctx context.Context, status HarnessAvailability, args []string) (*modelProbe, error) {
	cmd := exec.CommandContext(ctx, status.Executable, append(append([]string{}, status.Prefix...), args...)...)
	hideConsole(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = os.TempDir()
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	return &modelProbe{cmd: cmd, stdin: stdin, stdout: stdout, encoder: json.NewEncoder(stdin), scanner: scanner, stopWatch: context.AfterFunc(ctx, func() { stdout.Close() })}, nil
}

func (p *modelProbe) close() {
	p.stopWatch()
	p.stdin.Close()
	p.stdout.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
}

func (p *modelProbe) rpc(id int, method string, params any) (json.RawMessage, error) {
	if err := p.encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for count := 0; count < 256 && p.scanner.Scan(); count++ {
		var row struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(p.scanner.Bytes(), &row) != nil || row.ID == nil || *row.ID != id {
			continue
		}
		if len(row.Error) > 0 && string(row.Error) != "null" {
			if problem := providerDiagnostic("codex", string(row.Error)); problem != nil {
				return nil, problem
			}
			return nil, errors.New("Codex didn't list its models. Update Codex, or type a model name")
		}
		return row.Result, nil
	}
	return nil, errors.New("Codex model discovery did not return a response before the local limit; use the cached catalog or retry refresh")
}

func codexModels(ctx context.Context, status HarnessAvailability) (HarnessModelCatalog, error) {
	catalog := HarnessModelCatalog{Agent: "codex", Source: "Codex app-server model/list"}
	p, err := startModelProbe(ctx, status, []string{"app-server"})
	if err != nil {
		return catalog, err
	}
	defer p.close()
	if _, err = p.rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "nexus_desktop", "title": "Nexus", "version": "0.3.0"}}); err != nil {
		return catalog, err
	}
	if err = p.encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return catalog, err
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := p.rpc(page+2, "model/list", params)
		if err != nil {
			return catalog, err
		}
		var response struct {
			Data []struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				Name        string `json:"displayName"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
				Default     bool   `json:"isDefault"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return catalog, errors.New("Codex returned an unsupported model-list response")
		}
		for _, row := range response.Data {
			if row.Hidden {
				continue
			}
			id := row.Model
			if id == "" {
				id = row.ID
			}
			catalog.Items = append(catalog.Items, HarnessModel{ID: id, Name: row.Name, Description: row.Description, Default: row.Default})
		}
		if response.Next == nil || *response.Next == "" {
			break
		}
		cursor = *response.Next
		if seen[cursor] {
			return catalog, errors.New("Codex model-list pagination repeated a cursor")
		}
		seen[cursor] = true
		if page == 9 {
			return catalog, errors.New("Codex model catalog exceeded the pagination limit")
		}
	}
	return catalog, nil
}
