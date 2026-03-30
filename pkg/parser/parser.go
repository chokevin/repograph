package parser

import (
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
	for _, out := range outputs {
		for _, n := range out.result.Nodes {
			g.AddNode(n)
		}
		for _, e := range out.result.Edges {
			g.AddEdge(e)
		}
	}

	resolveCallEdges(g)
	buildContainmentHierarchy(g, repoPath)

	return g, nil
}

// resolveCallEdges replaces edges whose ToID starts with "unresolved:" with
// concrete edges pointing to every matching function/method node.
func resolveCallEdges(g *graph.Graph) {
	// Build name → []nodeID index from all function/method nodes.
	nameIndex := make(map[string][]string)
	for _, n := range g.Nodes() {
		if n.Type == graph.NodeFunction || n.Type == graph.NodeMethod {
			nameIndex[n.Name] = append(nameIndex[n.Name], n.ID)
		}
	}

	var toRemove []*graph.Edge
	var toAdd []*graph.Edge

	for _, e := range g.Edges() {
		if !strings.HasPrefix(e.ToID, "unresolved:") {
			continue
		}
		toRemove = append(toRemove, e)
		funcName := strings.TrimPrefix(e.ToID, "unresolved:")
		for _, targetID := range nameIndex[funcName] {
			toAdd = append(toAdd, &graph.Edge{
				FromID: e.FromID,
				ToID:   targetID,
				Type:   e.Type,
			})
		}
	}

	g.RemoveEdges(toRemove)
	for _, e := range toAdd {
		g.AddEdge(e)
	}
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
