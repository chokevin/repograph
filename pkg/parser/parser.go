package parser

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/chokevin/repograph/pkg/graph"
	"github.com/chokevin/repograph/pkg/scanner"

	// Import plugins so their init() functions register them.
	_ "github.com/chokevin/repograph/plugins/golang"
	_ "github.com/chokevin/repograph/plugins/javascript"
	_ "github.com/chokevin/repograph/plugins/python"
)

// Options controls graph-building behaviour.
type Options struct {
	Workers     int
	SkipDirs    []string
	MaxFileSize int64
}

// BuildGraph scans a repository and builds a complete code graph.
func BuildGraph(repoPath string, opts *Options) (*graph.Graph, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.Workers <= 0 {
		opts.Workers = 8
	}

	scanOpts := &scanner.Options{
		MaxFileSize: opts.MaxFileSize,
		SkipDirs:    opts.SkipDirs,
	}
	files, err := scanner.Scan(repoPath, scanOpts)
	if err != nil {
		return nil, err
	}

	g := graph.New()

	// Add a repo root node.
	repoName := filepath.Base(repoPath)
	g.AddNode(&graph.Node{
		ID:   "repo:" + repoName,
		Type: graph.NodeRepo,
		Name: repoName,
	})

	type parseJob struct {
		entry  scanner.FileEntry
		plugin graph.LanguagePlugin
	}
	type parseOutput struct {
		entry  scanner.FileEntry
		result *graph.ParseResult
	}

	jobs := make(chan parseJob, len(files))
	results := make(chan parseOutput, len(files))

	var wg sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parser := sitter.NewParser()
			for job := range jobs {
				parser.SetLanguage(job.plugin.Language())
				src, err := os.ReadFile(filepath.Join(repoPath, job.entry.Path))
				if err != nil {
					continue
				}
				tree, err := parser.ParseCtx(context.Background(), nil, src)
				if err != nil || tree == nil {
					continue
				}
				root := tree.RootNode()
				pr := job.plugin.Parse(job.entry.Path, src, root)
				results <- parseOutput{entry: job.entry, result: pr}
			}
		}()
	}

	// Enqueue jobs.
	for _, f := range files {
		ext := filepath.Ext(f.Path)
		p := graph.PluginForExtension(ext)
		if p == nil {
			continue
		}
		jobs <- parseJob{entry: f, plugin: p}
	}
	close(jobs)

	// Collect results.
	go func() {
		wg.Wait()
		close(results)
	}()

	var outputs []parseOutput
	for out := range results {
		// Add file node first.
		g.AddNode(&graph.Node{
			ID:       graph.FileNodeID(out.entry.Path),
			Type:     graph.NodeFile,
			Name:     filepath.Base(out.entry.Path),
			FilePath: out.entry.Path,
			Language: out.entry.Language,
		})
		if out.result != nil {
			outputs = append(outputs, parseOutput{out.entry, out.result})
		}
	}

	// Add symbol nodes and edges after all file nodes exist.
	// Collect unresolved edges separately — AddEdge drops them since targets don't exist yet.
	var unresolvedEdges []*graph.Edge
	for _, out := range outputs {
		for _, n := range out.result.Nodes {
			g.AddNode(n)
		}
		for _, e := range out.result.Edges {
			if strings.HasPrefix(e.ToID, "unresolved:") || strings.HasPrefix(e.ToID, "unresolved-import:") {
				unresolvedEdges = append(unresolvedEdges, e)
			} else {
				g.AddEdge(e)
			}
		}
	}

	buildContainmentHierarchy(g, repoPath)
	resolveEdges(g, unresolvedEdges, repoPath)

	return g, nil
}

// resolveEdges resolves all unresolved edge targets (calls, references, method_of, imports).
func resolveEdges(g *graph.Graph, unresolved []*graph.Edge, repoPath string) {
	// Build name indexes for resolution.
	funcIndex := make(map[string][]string)   // name → []nodeID (functions + methods)
	classIndex := make(map[string][]string)  // name → []nodeID (classes/structs)
	varIndex := make(map[string][]string)    // name → []nodeID (variables)
	for _, n := range g.Nodes() {
		switch n.Type {
		case graph.NodeFunction, graph.NodeMethod:
			funcIndex[n.Name] = append(funcIndex[n.Name], n.ID)
		case graph.NodeClass:
			classIndex[n.Name] = append(classIndex[n.Name], n.ID)
		case graph.NodeVariable:
			varIndex[n.Name] = append(varIndex[n.Name], n.ID)
		}
	}

	// Read go.mod for import path resolution.
	modulePath := readGoModulePath(repoPath)

	// Build dir → file index for Go import resolution.
	dirFiles := make(map[string][]string) // relative dir → []file node IDs
	for _, n := range g.NodesByType(graph.NodeFile) {
		dir := filepath.Dir(n.FilePath)
		dirFiles[dir] = append(dirFiles[dir], n.ID)
	}

	for _, e := range unresolved {
		if g.Node(e.FromID) == nil {
			continue
		}

		switch {
		case strings.HasPrefix(e.ToID, "unresolved-import:"):
			importPath := strings.TrimPrefix(e.ToID, "unresolved-import:")
			resolveImportEdge(g, e, importPath, modulePath, dirFiles)

		case strings.HasPrefix(e.ToID, "unresolved:"):
			name := strings.TrimPrefix(e.ToID, "unresolved:")
			switch e.Type {
			case graph.EdgeCalls:
				for _, targetID := range funcIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
			case graph.EdgeMethodOf:
				for _, targetID := range classIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
			case graph.EdgeInherits:
				for _, targetID := range classIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
			case graph.EdgeReferences:
				for _, targetID := range varIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
				// Also try functions/classes for references.
				for _, targetID := range funcIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
				for _, targetID := range classIndex[name] {
					g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: targetID, Type: e.Type})
				}
			}
		}
	}
}

// resolveImportEdge maps a Go module import path to file nodes in the matching dir.
func resolveImportEdge(g *graph.Graph, e *graph.Edge, importPath, modulePath string, dirFiles map[string][]string) {
	if modulePath == "" {
		return
	}
	if !strings.HasPrefix(importPath, modulePath+"/") {
		return // external dependency
	}
	relDir := strings.TrimPrefix(importPath, modulePath+"/")
	for _, fileID := range dirFiles[relDir] {
		g.AddEdge(&graph.Edge{FromID: e.FromID, ToID: fileID, Type: graph.EdgeImports})
	}
}

// readGoModulePath reads the module path from go.mod.
func readGoModulePath(repoPath string) string {
	f, err := os.Open(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// buildContainmentHierarchy adds REPO → DIR → FILE containment edges.
func buildContainmentHierarchy(g *graph.Graph, repoPath string) {
	repoName := filepath.Base(repoPath)
	repoID := "repo:" + repoName

	dirs := make(map[string]bool)

	for _, n := range g.NodesByType(graph.NodeFile) {
		dir := filepath.Dir(n.FilePath)
		// Ensure all ancestor directories exist as nodes.
		for d := dir; d != "." && d != ""; d = filepath.Dir(d) {
			if dirs[d] {
				break
			}
			dirs[d] = true
			g.AddNode(&graph.Node{
				ID:   graph.DirNodeID(d),
				Type: graph.NodeDir,
				Name: filepath.Base(d),
			})
		}

		// FILE → parent DIR (or REPO if top-level)
		if dir == "." {
			g.AddEdge(&graph.Edge{FromID: repoID, ToID: n.ID, Type: graph.EdgeContains})
		} else {
			g.AddEdge(&graph.Edge{FromID: graph.DirNodeID(dir), ToID: n.ID, Type: graph.EdgeContains})
		}
	}

	// Wire DIR → sub-DIR and REPO → top-level DIR.
	for d := range dirs {
		parent := filepath.Dir(d)
		if parent == "." || parent == "" {
			g.AddEdge(&graph.Edge{FromID: repoID, ToID: graph.DirNodeID(d), Type: graph.EdgeContains})
		} else {
			parentID := graph.DirNodeID(parent)
			if g.Node(parentID) != nil {
				g.AddEdge(&graph.Edge{FromID: parentID, ToID: graph.DirNodeID(d), Type: graph.EdgeContains})
			}
		}
	}
}
