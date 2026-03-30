package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chokevin/repograph/pkg/graph"
	_ "github.com/chokevin/repograph/plugins/golang"
	_ "github.com/chokevin/repograph/plugins/javascript"
	_ "github.com/chokevin/repograph/plugins/python"
)

func TestBuildGraphOnTempRepo(t *testing.T) {
	dir := t.TempDir()

	// Create test JS files
	writeFile(t, dir, "main.js", `const { greet } = require('./utils');

function start() {
  greet('world');
}

module.exports = { start };
`)
	writeFile(t, dir, "utils.js", `function greet(name) {
  console.log('Hello ' + name);
}

module.exports = { greet };
`)

	g, err := BuildGraph(dir, nil)
	if err != nil {
		t.Fatalf("BuildGraph failed: %v", err)
	}

	if g.NodeCount() < 4 {
		t.Errorf("expected at least 4 nodes (2 files + 2 funcs), got %d", g.NodeCount())
	}

	// Check import edge exists
	hasImport := false
	for _, e := range g.Edges() {
		if e.Type == graph.EdgeImports {
			hasImport = true
			break
		}
	}
	if !hasImport {
		t.Error("expected at least one import edge")
	}

	// Check related files
	related := g.RelatedFiles("main.js", 1)
	if len(related) == 0 {
		t.Error("expected main.js to have related files")
	}

	t.Logf("Graph: %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
	t.Logf("Summary:\n%s", g.Summary())
}

func TestBuildGraphOnGoFiles(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "main.go", `package main

import "fmt"

func main() {
	fmt.Println(greet("world"))
}
`)
	writeFile(t, dir, "util.go", `package main

func greet(name string) string {
	return "Hello " + name
}
`)

	g, err := BuildGraph(dir, nil)
	if err != nil {
		t.Fatalf("BuildGraph failed: %v", err)
	}

	funcs := g.NodesByType(graph.NodeFunction)
	if len(funcs) < 2 {
		t.Errorf("expected at least 2 functions, got %d", len(funcs))
	}

	t.Logf("Graph: %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
}

func TestBuildGraphOnPythonFiles(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "app.py", `from utils import greet

class App:
    def run(self):
        greet("world")

x = 42
`)
	writeFile(t, dir, "utils.py", `def greet(name):
    print(f"Hello {name}")
`)

	g, err := BuildGraph(dir, nil)
	if err != nil {
		t.Fatalf("BuildGraph failed: %v", err)
	}

	classes := g.NodesByType(graph.NodeClass)
	if len(classes) < 1 {
		t.Errorf("expected at least 1 class, got %d", len(classes))
	}

	t.Logf("Graph: %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
