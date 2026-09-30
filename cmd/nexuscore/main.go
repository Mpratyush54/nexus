// Command nexuscore is the in-process engine entry (spec 7.8).
// It reads one JSON object {"method","args"} from stdin and writes the
// nx_call result. C# and Swift bindings call the same method names.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"central-memory/internal/core"
)

func main() {
	var req struct {
		Method string          `json:"method"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintf(os.Stderr, "nexuscore: %v\n", err)
		os.Exit(2)
	}
	out, err := core.Call(context.Background(), req.Method, req.Args, core.Deps{})
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
	} else {
		resp["result"] = out
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "nexuscore: %v\n", err)
		os.Exit(1)
	}
	if resp["ok"] != true {
		os.Exit(1)
	}
}
