package rootbroker

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBrokerStopsFloodingHelperBeforeBufferingIt runs a helper that writes
// without end. The broker must fail the operation promptly instead of
// buffering the whole stream and checking its length afterwards.
func TestBrokerStopsFloodingHelperBeforeBufferingIt(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	fakeAppctl := filepath.Join(t.TempDir(), "appctl")
	if err := os.WriteFile(fakeAppctl, []byte("#!/bin/sh\nexec yes\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = fakeAppctl
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	response, err := broker.Execute(ctx, &Request{
		RequestType: "task",
		Task:        &TaskRequest{Action: "history", Site: "demo", Name: "nightly"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "exceeds limit") {
		t.Fatalf("flooding helper response = %#v; want output-limit failure", response)
	}
	if len(response.Error) > maxBrokerCommandOutput+1024 {
		t.Fatalf("error carried %d bytes of output", len(response.Error))
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("flooding helper was not stopped promptly")
	}
}

// TestBrokerNeverBuffersSubprocessOutputUnbounded keeps the invariant
// structural: broker code must run subprocesses through the capped runner.
func TestBrokerNeverBuffersSubprocessOutputUnbounded(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if assign, ok := node.(*ast.AssignStmt); ok {
				for _, expr := range assign.Lhs {
					selector, ok := expr.(*ast.SelectorExpr)
					if ok && (selector.Sel.Name == "Stdout" || selector.Sel.Name == "Stderr") {
						t.Errorf("%s: direct cmd.%s assignment bypasses output limits; use stepanelhelper.RunCapped", fset.Position(expr.Pos()), selector.Sel.Name)
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "CombinedOutput" || sel.Sel.Name == "Output") && len(call.Args) == 0 {
				t.Errorf("%s: unbounded %s(); use stepanelhelper.RunCapped", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
