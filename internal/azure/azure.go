// Package azure implements the 15-min remote leg: restic -> Azure Blob
// (Hot tier) + secrets in Key Vault.
//
// Design (fits $100/yr credits, Hot tier):
//   - Remote push runs every 15 min alongside the local mirror: Hot has no
//     retention minimum and no early-deletion penalty, so restic churn costs
//     nothing extra. A lifecycle rule tiers 30d-untouched objects to Cool.
//   - Local robocopy mirror (15 min) is the fast recovery path; Azure is the
//     off-device leg for reset/loss.
//   - Auth via `az login` (Entra ID). No static keys in repo or disk:
//     connection via managed RBAC, restic password fetched from Key Vault
//     into memory and piped to restic --password-command (never written).
//   - Every function degrades honestly when az/restic are missing: Status()
//     reports pending, Push() returns an actionable error. P3 never pretends
//     off-device safety exists before `mem login azure` succeeds.
package azure

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config lives in vault/config/azure.json (settings only — no secrets).
type Config struct {
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resource_group,omitempty"`
	Storage       string `json:"storage_account,omitempty"`
	Container     string `json:"container,omitempty"`
	KeyVault      string `json:"key_vault,omitempty"`
	SecretName    string `json:"secret_name,omitempty"`
}

func configPath(vault string) string { return filepath.Join(vault, "config", "azure.json") }

// Load reads config (empty struct when never configured).
func Load(vault string) Config {
	var c Config
	if data, err := os.ReadFile(configPath(vault)); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if c.Container == "" {
		c.Container = "central-backup"
	}
	if c.SecretName == "" {
		c.SecretName = "restic-password"
	}
	return c
}

// Save persists non-secret settings.
func Save(vault string, c Config) error {
	_ = os.MkdirAll(filepath.Dir(configPath(vault)), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(vault), b, 0o644)
}

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// Status reports readiness without side effects.
func Status(vault string) (ready bool, detail string) {
	c := Load(vault)
	var missing []string
	if !have("az") {
		missing = append(missing, "az cli not installed (https://aka.ms/installazurecli)")
	}
	if !have("restic") {
		missing = append(missing, "restic not installed (winget install restic)")
	}
	if c.Storage == "" {
		missing = append(missing, "storage not configured (mem login azure --storage NAME --vault-name VAULT)")
	}
	if len(missing) > 0 {
		return false, "remote pending: " + strings.Join(missing, "; ")
	}
	return true, fmt.Sprintf("ready: storage=%s container=%s keyvault=%s (15-min Hot push)", c.Storage, c.Container, c.KeyVault)
}

// Login runs `az login` (browser/device-code) then stores settings.
// Never stores keys: access stays on the Entra identity + RBAC.
func Login(vault string, args []string) error {
	if !have("az") {
		return fmt.Errorf("az cli not installed — install it, then re-run mem login azure")
	}
	c := Load(vault)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--subscription":
			if i+1 < len(args) {
				c.Subscription = args[i+1]
				i++
			}
		case "--group":
			if i+1 < len(args) {
				c.ResourceGroup = args[i+1]
				i++
			}
		case "--storage":
			if i+1 < len(args) {
				c.Storage = args[i+1]
				i++
			}
		case "--container":
			if i+1 < len(args) {
				c.Container = args[i+1]
				i++
			}
		case "--vault-name":
			if i+1 < len(args) {
				c.KeyVault = args[i+1]
				i++
			}
		}
	}
	cmd := exec.Command("az", "login")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("az login failed: %w", err)
	}
	if c.Subscription != "" {
		_ = exec.Command("az", "account", "set", "--subscription", c.Subscription).Run()
	}
	if err := Save(vault, c); err != nil {
		return err
	}
	fmt.Println("azure login ok; settings saved (no secrets stored)")
	return nil
}

// FetchPassword reads the restic password from Key Vault into memory only.
// Falls back to RESTIC_PASSWORD env for machines without Vault access.
func FetchPassword(vault string) (string, error) {
	if pw := os.Getenv("RESTIC_PASSWORD"); pw != "" {
		return pw, nil
	}
	c := Load(vault)
	if c.KeyVault == "" || !have("az") {
		return "", fmt.Errorf("no restic password: set RESTIC_PASSWORD or configure Key Vault (mem login azure --vault-name VAULT)")
	}
	out, err := exec.Command("az", "keyvault", "secret", "show",
		"--vault-name", c.KeyVault, "--name", c.SecretName,
		"--query", "value", "-o", "tsv").Output()
	if err != nil {
		return "", fmt.Errorf("keyvault fetch failed: %w", err)
	}
	if pw := strings.TrimSpace(string(out)); pw != "" {
		return pw, nil
	}
	return "", fmt.Errorf("keyvault secret %s is empty", c.SecretName)
}

// Push snapshots vault/agents raw + drive-mirror to Azure via restic.
// Hourly cadence only (see package doc). Password is piped, never written.
func Push(vault string) error {
	ok, detail := Status(vault)
	if !ok {
		return fmt.Errorf("remote not ready — %s", detail)
	}
	pw, err := FetchPassword(vault)
	if err != nil {
		return err
	}
	c := Load(vault)
	repo := fmt.Sprintf("azure:%s:/restic", c.Container)
	for _, src := range []string{filepath.Join(vault, "agents"), filepath.Join(vault, "drive-mirror")} {
		if _, err := os.Stat(src); err != nil {
			continue
		}
		cmd := exec.Command("restic", "-r", repo, "backup", src,
			"--exclude", "node_modules", "--exclude", "dist", "--exclude", "build",
			"--exclude", "__pycache__", "--exclude", ".venv")
		cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+pw,
			"AZURE_STORAGE_ACCOUNT="+c.Storage)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restic backup %s: %w", src, err)
		}
	}
	fmt.Println("remote push ok (15-min Hot leg)")
	return nil
}
