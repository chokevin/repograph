package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/chokevin/repograph/pkg/graph"
	"github.com/chokevin/repograph/pkg/parser"
	"github.com/chokevin/repograph/pkg/query"

	// Register language plugins via init().
	_ "github.com/chokevin/repograph/plugins/golang"
	_ "github.com/chokevin/repograph/plugins/javascript"
	_ "github.com/chokevin/repograph/plugins/python"
)

func main() {
	repo := flag.String("repo", ".", "Repository path")
	action := flag.String("action", "", "Action: build, summary, related, context, prompt, file, decompose, constraints")
	file := flag.String("file", "", "File path (for related, prompt, file actions)")
	queryStr := flag.String("query", "", "Search query (for context action)")
	depth := flag.Int("depth", 2, "Hop depth (for related action)")
	format := flag.String("format", "text", "Output format: text or json")
	flag.Parse()

	if *action == "" {
		flag.Usage()
		os.Exit(1)
	}

	switch *action {
	case "build":
		runBuild(*repo, *format)
	case "summary":
		runSummary(*repo)
	case "related":
		requireFlag("file", *file)
		runRelated(*repo, *file, *depth, *format)
	case "context":
		requireFlag("query", *queryStr)
		runContext(*repo, *queryStr, *format)
	case "prompt":
		requireFlag("file", *file)
		runPrompt(*repo, *file, *depth)
	case "file":
		requireFlag("file", *file)
		runFile(*repo, *file, *format)
	case "decompose":
		requireFlag("file", *file)
		runDecompose(*repo, *file, *format)
	case "constraints":
		requireFlag("file", *file)
		runConstraints(*repo, *file, *format)
	default:
		fatal("unknown action: %s", *action)
	}
}

func requireFlag(name, value string) {
	if value == "" {
		fatal("--%s is required for this action", name)
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func buildGraph(repoPath string) *query.Result {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	return &query.Result{Graph: g, Nodes: g.Nodes(), Edges: g.Edges()}
}

func runBuild(repoPath, format string) {
	start := time.Now()
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	elapsed := time.Since(start).Milliseconds()

	if format == "json" {
		out := map[string]interface{}{
			"build_ms":   elapsed,
			"node_count": g.NodeCount(),
			"edge_count": g.EdgeCount(),
			"languages":  g.Languages(),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		fmt.Printf("Built graph in %dms\n", elapsed)
		fmt.Printf("  Nodes: %d\n", g.NodeCount())
		fmt.Printf("  Edges: %d\n", g.EdgeCount())
		langs := g.Languages()
		if len(langs) > 0 {
			fmt.Printf("  Languages: %s\n", join(langs))
		}
	}
}

func runSummary(repoPath string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	fmt.Print(g.Summary())
}

func runRelated(repoPath, filePath string, depth int, format string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	result := query.QueryRelated(g, filePath, depth)
	outputResult(result, format)
}

func runContext(repoPath, queryStr, format string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	result := query.QueryContext(g, queryStr)
	outputResult(result, format)
}

func runPrompt(repoPath, filePath string, depth int) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	result := query.QueryFile(g, filePath)

	// Also pull in related files for richer context
	related := query.QueryRelated(g, filePath, depth)
	merged := mergeResults(result, related)
	fmt.Print(query.FormatForPrompt(merged))
}

func runFile(repoPath, filePath, format string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	result := query.QueryFile(g, filePath)
	outputResult(result, format)
}

func runDecompose(repoPath, filePath, format string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	dr := query.QueryDecompose(g, filePath)
	if dr == nil {
		fatal("file not found in graph: %s", filePath)
	}
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(dr)
	} else {
		fmt.Print(query.FormatDecompose(dr))
	}
}

func runConstraints(repoPath, filePath, format string) {
	g, err := parser.BuildGraph(repoPath, nil)
	if err != nil {
		fatal("build failed: %v", err)
	}
	cr := query.QueryConstraints(g, filePath)
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cr)
	} else {
		fmt.Print(query.FormatConstraints(cr))
	}
}

func outputResult(r *query.Result, format string) {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
	} else {
		fmt.Print(query.FormatForPrompt(r))
	}
}

func mergeResults(a, b *query.Result) *query.Result {
	// Deduplicate nodes
	nodeSeen := make(map[string]bool)
	var nodes []*graph.Node
	for _, n := range a.Nodes {
		if !nodeSeen[n.ID] {
			nodeSeen[n.ID] = true
			nodes = append(nodes, n)
		}
	}
	for _, n := range b.Nodes {
		if !nodeSeen[n.ID] {
			nodeSeen[n.ID] = true
			nodes = append(nodes, n)
		}
	}

	// Deduplicate edges
	type edgeKey struct{ from, to string; t graph.EdgeType }
	edgeSeen := make(map[edgeKey]bool)
	var edges []*graph.Edge
	for _, e := range a.Edges {
		k := edgeKey{e.FromID, e.ToID, e.Type}
		if !edgeSeen[k] {
			edgeSeen[k] = true
			edges = append(edges, e)
		}
	}
	for _, e := range b.Edges {
		k := edgeKey{e.FromID, e.ToID, e.Type}
		if !edgeSeen[k] {
			edgeSeen[k] = true
			edges = append(edges, e)
		}
	}

	// Deduplicate files
	fileSeen := make(map[string]bool)
	var files []string
	for _, f := range a.Files {
		if !fileSeen[f] {
			fileSeen[f] = true
			files = append(files, f)
		}
	}
	for _, f := range b.Files {
		if !fileSeen[f] {
			fileSeen[f] = true
			files = append(files, f)
		}
	}

	g := a.Graph
	if g == nil {
		g = b.Graph
	}

	return &query.Result{
		Graph: g,
		Nodes: nodes,
		Edges: edges,
		Files: files,
	}
}

func join(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
