package graph

import (
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestNewGraph(t *testing.T) {
	g := New()
	if g.NodeCount() != 0 {
		t.Errorf("expected 0 nodes, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Errorf("expected 0 edges, got %d", g.EdgeCount())
	}
}

func TestAddNodeAndEdge(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "file:a.js", Type: NodeFile, Name: "a.js", FilePath: "a.js", Language: "javascript"})
	g.AddNode(&Node{ID: "file:b.js", Type: NodeFile, Name: "b.js", FilePath: "b.js", Language: "javascript"})
	g.AddNode(&Node{ID: "func:a.js:foo", Type: NodeFunction, Name: "foo", FilePath: "a.js", Exported: true})

	if g.NodeCount() != 3 {
		t.Errorf("expected 3 nodes, got %d", g.NodeCount())
	}

	g.AddEdge(&Edge{FromID: "file:a.js", ToID: "file:b.js", Type: EdgeImports})
	g.AddEdge(&Edge{FromID: "file:a.js", ToID: "func:a.js:foo", Type: EdgeDefines})

	if g.EdgeCount() != 2 {
		t.Errorf("expected 2 edges, got %d", g.EdgeCount())
	}

	// Duplicate node
	if g.AddNode(&Node{ID: "file:a.js", Type: NodeFile, Name: "a.js"}) {
		t.Error("duplicate node should return false")
	}

	// Edge with missing endpoint
	if g.AddEdge(&Edge{FromID: "file:a.js", ToID: "file:missing.js", Type: EdgeImports}) {
		t.Error("edge with missing endpoint should return false")
	}
}

func TestRelatedFiles(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "file:a.js", Type: NodeFile, Name: "a.js", FilePath: "a.js"})
	g.AddNode(&Node{ID: "file:b.js", Type: NodeFile, Name: "b.js", FilePath: "b.js"})
	g.AddNode(&Node{ID: "file:c.js", Type: NodeFile, Name: "c.js", FilePath: "c.js"})
	g.AddEdge(&Edge{FromID: "file:a.js", ToID: "file:b.js", Type: EdgeImports})
	g.AddEdge(&Edge{FromID: "file:b.js", ToID: "file:c.js", Type: EdgeImports})

	related := g.RelatedFiles("a.js", 1)
	if len(related) != 1 || related[0].FilePath != "b.js" {
		t.Errorf("depth 1: expected [b.js], got %v", related)
	}

	related = g.RelatedFiles("a.js", 2)
	if len(related) != 2 {
		t.Errorf("depth 2: expected 2 files, got %d", len(related))
	}
}

func TestSubgraph(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "file:a.js", Type: NodeFile, Name: "a.js", FilePath: "a.js"})
	g.AddNode(&Node{ID: "file:b.js", Type: NodeFile, Name: "b.js", FilePath: "b.js"})
	g.AddNode(&Node{ID: "file:c.js", Type: NodeFile, Name: "c.js", FilePath: "c.js"})
	g.AddEdge(&Edge{FromID: "file:a.js", ToID: "file:b.js", Type: EdgeImports})
	g.AddEdge(&Edge{FromID: "file:b.js", ToID: "file:c.js", Type: EdgeImports})

	sub := g.Subgraph([]string{"file:a.js", "file:b.js"})
	if sub.NodeCount() != 2 {
		t.Errorf("expected 2 nodes, got %d", sub.NodeCount())
	}
	if sub.EdgeCount() != 1 {
		t.Errorf("expected 1 edge, got %d", sub.EdgeCount())
	}
}

func TestSearch(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "func:a.js:loginUser", Type: NodeFunction, Name: "loginUser", FilePath: "a.js"})
	g.AddNode(&Node{ID: "func:b.js:logoutUser", Type: NodeFunction, Name: "logoutUser", FilePath: "b.js"})
	g.AddNode(&Node{ID: "func:c.js:getUser", Type: NodeFunction, Name: "getUser", FilePath: "c.js"})

	results := g.Search("login")
	if len(results) != 1 || results[0].Name != "loginUser" {
		t.Errorf("expected [loginUser], got %v", results)
	}

	results = g.Search("user")
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
}

func TestLanguages(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "file:a.js", Type: NodeFile, FilePath: "a.js", Language: "javascript"})
	g.AddNode(&Node{ID: "file:b.go", Type: NodeFile, FilePath: "b.go", Language: "go"})
	g.AddNode(&Node{ID: "file:c.js", Type: NodeFile, FilePath: "c.js", Language: "javascript"})

	langs := g.Languages()
	if len(langs) != 2 {
		t.Errorf("expected 2 languages, got %d: %v", len(langs), langs)
	}
}

func TestPluginRegistry(t *testing.T) {
	// Plugins are registered via init() — check that the registry works
	p := &testPlugin{}
	RegisterPlugin(p)

	if got := PluginForExtension(".test"); got == nil {
		t.Error("expected plugin for .test extension")
	}
	if got := PluginForName("test"); got == nil {
		t.Error("expected plugin for name 'test'")
	}
	if got := LanguageForExtension(".test"); got != "test" {
		t.Errorf("expected language 'test', got %q", got)
	}
}

type testPlugin struct{}

func (p *testPlugin) Name() string                                                        { return "test" }
func (p *testPlugin) Extensions() []string                                                { return []string{".test"} }
func (p *testPlugin) Language() *sitter.Language                                           { return nil }
func (p *testPlugin) Parse(filePath string, source []byte, root *sitter.Node) *ParseResult { return nil }
