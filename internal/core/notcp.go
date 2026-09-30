// Package core — no-TCP gate helpers (spec 7.5 / P0 acceptance j).
//
// The default nexuscore / embedded engine must not open a localhost HTTP
// listener. Outbound HTTPS to the cloud API is fine. Optional escape hatch:
// set NEXUS_NO_TCP=1 in processes that should refuse Listen (capture
// foreground stub checks this). There is no build tag that adds a TCP
// server to nexuscore; do not introduce one.
package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ErrTCPListenRefused is returned when code tries to open a local TCP
// listener under NEXUS_NO_TCP=1.
var ErrTCPListenRefused = fmt.Errorf("NEXUS_NO_TCP: local TCP listen is retired; use embedded nexuscore (no :7272)")

// NoTCPEnabled reports whether NEXUS_NO_TCP is set to a truthy value.
func NoTCPEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("NEXUS_NO_TCP")))
	return v == "1" || v == "true" || v == "yes"
}

// RefuseListen returns ErrTCPListenRefused when NEXUS_NO_TCP is enabled.
func RefuseListen() error {
	if NoTCPEnabled() {
		return ErrTCPListenRefused
	}
	return nil
}

// SourceMentionsTCPListen reports whether any .go file under roots contains
// a call to ListenAndServe or net.Listen / http.ListenAndServe-style
// identifiers used as the daemon TCP gate. Used by tests to guard nexuscore.
func SourceMentionsTCPListen(roots ...string) ([]string, error) {
	var hits []string
	fset := token.NewFileSet()
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				if base == "testdata" || base == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(call.Fun)
				switch name {
				case "ListenAndServe", "ListenAndServeTLS", "Listen":
					// net.Listen / http.Server.ListenAndServe
					hits = append(hits, fmt.Sprintf("%s: %s", path, name))
				}
				return true
			})
			return nil
		})
		if err != nil {
			return hits, err
		}
	}
	return hits, nil
}

func callName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}
