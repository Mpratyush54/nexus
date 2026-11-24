// Command nexus-demo runs the normal Nexus API against an isolated in-memory
// development store. It never connects to production, sends email or executes
// a harness. Seeded conversation text is synthetic, not a claimed live reply.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"central-memory/internal/core"
	"central-memory/internal/server"
	"central-memory/internal/store"
)

const sender = "Avery (Demo)"
const recipient = "Maya (Demo)"

type demoState struct {
	URL            string `json:"url"`
	SessionID      string `json:"session_id"`
	ProjectID      string `json:"project_id"`
	OrganizationID string `json:"organization_id"`
	Workspace      string `json:"workspace"`
	SenderToken    string `json:"sender_token"`
	RecipientToken string `json:"recipient_token"`
	TeleportID     string `json:"teleport_id,omitempty"`
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func seed(workspace, url string) (*server.Server, demoState, error) {
	ctx := context.Background()
	mem := store.NewMemStore()
	srv := server.NewServer(mem)
	tokens, err := newDemoTokenStore()
	if err != nil {
		return nil, demoState{}, err
	}
	srv.Tokens = tokens
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, demoState{}, err
	}
	srv.Auth = server.NewAuthenticator(key)
	org, err := mem.CreateOrganization(ctx, "Nexus Demo Team", "nexus-film-demo", sender)
	if err != nil {
		return nil, demoState{}, err
	}
	if _, err = mem.AddOrgMember(ctx, org.ID, recipient, "MEMBER", sender); err != nil {
		return nil, demoState{}, err
	}
	project, err := mem.CreateOrgProject(ctx, org.ID, "checkout-demo", "Checkout · Demo workspace", "", "", sender)
	if err != nil {
		return nil, demoState{}, err
	}
	if err = mem.GrantMember(ctx, project.ID, recipient, sender); err != nil {
		return nil, demoState{}, err
	}
	now := time.Now().UTC()
	for _, item := range []*store.MemoryItem{
		{ProjectID: project.ID, Key: "checkout/cart-preservation", Content: "Preserve the cart until payment is confirmed so customers can recover from a failed checkout without rebuilding their order.", Level: "project", Scope: "decision", Category: "architecture", Status: "CONFIRMED", Source: "cursor", Tags: []string{"checkout", "recovery", "pinned"}, FilesAffected: []string{"src/checkout.ts"}},
		{ProjectID: project.ID, OrgID: org.ID, Key: "team/release-verification", Content: "Before merging a payment change, review the recovery patch and verify failure-path coverage with a teammate.", Level: "organization", Scope: "constraint", Category: "conventions", Status: "CONFIRMED", Source: "team", Tags: []string{"review", "release"}},
		{ProjectID: project.ID, UserID: sender, Key: "personal/review-style", Content: "Summarize the decision and next verification step before handing a task to another teammate.", Level: "personal", Scope: "preference", Category: "general", Status: "CONFIRMED", Source: "demo", Visibility: "private"},
	} {
		if err = mem.CreateMemoryItem(ctx, item); err != nil {
			return nil, demoState{}, err
		}
	}
	session, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: sender, Harness: "cursor", NativeID: "nexus-demo-checkout", Title: "Checkout recovery · preserve the cart", Summary: "Decision: keep the cart until payment is confirmed. Review the recovery patch and failure-path coverage before merging.", WorkspaceRootHint: workspace, StartedAt: now, LastActiveAt: now, Visibility: "private"})
	if err != nil {
		return nil, demoState{}, err
	}
	patch := "--- a/src/checkout.ts\n+++ b/src/checkout.ts\n@@ -1,3 +1,3 @@\n export function finishCheckout(payment, clearCart) {\n-  clearCart();\n+  if (payment.confirmed) clearCart();\n }"
	tools, _ := json.Marshal([]any{map[string]any{"name": "edit", "args": map[string]any{"file_path": "src/checkout.ts", "diff": patch}, "result": "Recorded change: preserve the cart until payment is confirmed."}})
	turns := []store.SessionTurn{
		{SessionID: session.ID, Idx: 1, Role: "user", At: now, SourceAt: &now, TextPreview: "Keep a failed checkout recoverable. Preserve the cart instead of clearing it.", ToolCalls: json.RawMessage("[]")},
		{SessionID: session.ID, Idx: 2, Role: "assistant", At: now, SourceAt: &now, TextPreview: "The decision: keep the cart until payment is confirmed.\n\nI have recorded the recovery change with this conversation. Next: review failure-path coverage before merging.", ToolCalls: json.RawMessage("[]")},
		{SessionID: session.ID, Idx: 3, Role: "assistant", At: now, SourceAt: &now, ToolCalls: tools},
	}
	for _, turn := range turns {
		if err = mem.AppendSessionTurn(ctx, turn); err != nil {
			return nil, demoState{}, err
		}
	}
	transcript, _ := json.Marshal(turns)
	sum := sha256.Sum256(transcript)
	hash := hex.EncodeToString(sum[:])
	if err = mem.PutBlob(ctx, project.ID, hash, "plain", "transcript", transcript); err != nil {
		return nil, demoState{}, err
	}
	version, err := mem.CreateSessionVersion(ctx, session.ID, sender)
	if err != nil {
		return nil, demoState{}, err
	}
	manifest, _ := json.Marshal(map[string]any{"harness": "cursor", "native_id": session.NativeID, "transcript": map[string]string{"blob": "sha256:" + hash}, "files": []any{}})
	if _, err = mem.CompleteSessionVersion(ctx, session.ID, version.Version, manifest); err != nil {
		return nil, demoState{}, err
	}
	st := demoState{URL: url, SessionID: session.ID, ProjectID: project.ID, OrganizationID: org.ID, Workspace: workspace}
	st.SenderToken, err = srv.Auth.GenerateUser(sender, sender, time.Hour*4)
	if err != nil {
		return nil, st, err
	}
	st.RecipientToken, err = srv.Auth.GenerateUser(recipient, recipient, time.Hour*4)
	return srv, st, err
}

func configs(dir string, st demoState) error {
	for label, identity := range map[string]struct{ token, name string }{"sender": {st.SenderToken, sender}, "recipient": {st.RecipientToken, recipient}} {
		if err := writeJSON(filepath.Join(dir, label, "config.json"), map[string]any{"server_url": st.URL, "token": identity.token, "user_id": identity.name, "username": identity.name, "capture_workspaces": []string{}}); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(dir, "state.json"), st)
}

func flow(ctx context.Context, dir, action string) error {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return err
	}
	var st demoState
	if err = json.Unmarshal(b, &st); err != nil {
		return err
	}
	cloud := &core.HTTPCloud{BaseURL: st.URL, Token: st.SenderToken}
	if action == "memory" {
		if st.URL != "http://127.0.0.1:47831" {
			return errors.New("MCP film proof is restricted to the isolated loopback demo")
		}
		proof, err := memoryProof(ctx, cloud, st.ProjectID)
		if err != nil {
			return err
		}
		if err = writeJSON(filepath.Join(dir, "mcp-proof.json"), proof); err != nil {
			return err
		}
		fmt.Println("Verified actual MCP memory_search and project_knowledge returned the synthetic checkout decision; credential-free proof saved.")
		return nil
	}
	if action == "send" {
		args, _ := json.Marshal(map[string]any{"session_id": st.SessionID, "to_user_id": recipient, "include_code": true, "workspace_root": st.Workspace})
		got, e := core.Call(ctx, "teleport.send", args, core.Deps{Cloud: cloud, Root: st.Workspace, Online: true})
		if e != nil {
			return e
		}
		obj, ok := got.(map[string]any)
		if !ok {
			return errors.New("unexpected send result")
		}
		st.TeleportID, _ = obj["id"].(string)
		if st.TeleportID == "" {
			return errors.New("missing transfer id")
		}
		if err = writeJSON(filepath.Join(dir, "state.json"), st); err != nil {
			return err
		}
		fmt.Printf("Real demo transfer created; code files: %v\n", obj["code_files"])
		return nil
	}
	cloud.Token = st.RecipientToken
	if st.TeleportID == "" {
		return errors.New("send the demo first")
	}
	if action == "accept" {
		_, err = cloud.Do(ctx, http.MethodPost, "/v1/teleports/"+st.TeleportID+"/accept", []byte("{}"))
		if err == nil {
			fmt.Println("Demo recipient accepted through the real API")
		}
		return err
	}
	if action == "restore" {
		dest := filepath.Join(dir, "restored-code")
		args, _ := json.Marshal(map[string]string{"session_id": st.SessionID, "dest": dest})
		got, e := core.Call(ctx, "teleport.restore_code", args, core.Deps{Cloud: cloud, Online: true})
		if e != nil {
			return e
		}
		err = filepath.WalkDir(st.Workspace, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			rel, e := filepath.Rel(st.Workspace, path)
			if e != nil {
				return e
			}
			a, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			b, e := os.ReadFile(filepath.Join(dest, rel))
			if e != nil {
				return e
			}
			if sha256.Sum256(a) != sha256.Sum256(b) {
				return fmt.Errorf("restored file mismatch: %s", rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Printf("Verified demo restore and source hashes: %v\n", got)
		return nil
	}
	return errors.New("action must be serve, send, accept, restore or memory")
}

func main() {
	dir := flag.String("state-dir", "", "dedicated demo state directory (required)")
	workspace := flag.String("workspace", "", "synthetic demo code folder for serve")
	action := flag.String("action", "serve", "serve|send|accept|restore|memory")
	webRoot := flag.String("web-root", "", "optional built real frontend directory, served only on the isolated loopback listener")
	flag.Parse()
	if *dir == "" {
		log.Fatal("--state-dir is required; production config is never used")
	}
	if *action != "serve" {
		if err := flow(context.Background(), *dir, *action); err != nil {
			log.Fatal(err)
		}
		return
	}
	root, err := filepath.Abs(*workspace)
	if err != nil || *workspace == "" {
		log.Fatal("--workspace is required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:47831")
	if err != nil {
		log.Fatal(err)
	}
	srv, st, err := seed(root, "http://127.0.0.1:47831")
	if err != nil {
		log.Fatal(err)
	}
	if err = configs(*dir, st); err != nil {
		log.Fatal(err)
	}
	log.Print("Isolated Nexus demo: real API handlers, two synthetic identities, loopback only. No production connection.")
	log.Fatal((&http.Server{Handler: demoWebHandler(srv.Handler(), st, *webRoot), ReadHeaderTimeout: 5 * time.Second}).Serve(listener))
}
