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
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(json.RawMessage(core.MarshalResult(out, err))); err != nil {
		fmt.Fprintf(os.Stderr, "nexuscore: %v\n", err)
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
