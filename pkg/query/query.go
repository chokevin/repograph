package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chokevin/repograph/pkg/graph"
)

// Result holds the output of a query against the code graph.
type Result struct {
	Graph *graph.Graph `json:"-"`
	Nodes []*graph.Node `json:"nodes"`
	Edges []*graph.Edge `json:"edges"`
	Files []string      `json:"files"`
}

// QueryRelated finds files related to filePath within depth hops and returns
// a subgraph containing those files and their symbols.
func QueryRelated(g *graph.Graph, filePath string, depth int) *Result {
	related := g.RelatedFiles(filePath, depth)

	// Collect file paths and all node IDs for the related files
	ids := make(map[string]bool)
	var files []string

	// Include the source file itself
	srcID := graph.FileNodeID(filePath)
	if n := g.Node(srcID); n != nil {
		ids[srcID] = true
		files = append(files, filePath)
	}
	for _, n := range g.Nodes() {
		if n.FilePath == filePath {
			ids[n.ID] = true
		}
	}

	// Include related files and their symbols
	for _, fn := range related {
		ids[fn.ID] = true
		files = append(files, fn.FilePath)
		for _, n := range g.Nodes() {
			if n.FilePath == fn.FilePath {
				ids[n.ID] = true
			}
		}
	}

	sort.Strings(files)
	nodeIDs := mapKeys(ids)
	sub := g.Subgraph(nodeIDs)

	return &Result{
		Graph: sub,
		Nodes: sub.Nodes(),
		Edges: sub.Edges(),
		Files: files,
	}
}

// QueryContext performs a keyword-scored search across the graph.
// Nodes are scored: +3 for name match, +2 for file path match, +1 for metadata match.
// Returns the top 50 results as a subgraph.
func QueryContext(g *graph.Graph, queryString string) *Result {
	keywords := strings.Fields(strings.ToLower(queryString))
	if len(keywords) == 0 {
		return &Result{Graph: graph.New()}
	}

	type scored struct {
		node  *graph.Node
		score int
	}
	var results []scored

	for _, n := range g.Nodes() {
		score := 0
		nameLower := strings.ToLower(n.Name)
		pathLower := strings.ToLower(n.FilePath)

		for _, kw := range keywords {
			if strings.Contains(nameLower, kw) {
				score += 3
			}
			if strings.Contains(pathLower, kw) {
				score += 2
			}
			for _, v := range n.Metadata {
				if strings.Contains(strings.ToLower(v), kw) {
					score++
				}
			}
		}

		if score > 0 {
			results = append(results, scored{node: n, score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].node.ID < results[j].node.ID
	})

	limit := 50
	if len(results) < limit {
		limit = len(results)
	}
	results = results[:limit]

	ids := make([]string, len(results))
	fileSet := make(map[string]bool)
	nodes := make([]*graph.Node, len(results))
	for i, r := range results {
		ids[i] = r.node.ID
		nodes[i] = r.node
		if r.node.FilePath != "" {
			fileSet[r.node.FilePath] = true
		}
	}

	sub := g.Subgraph(ids)
	files := mapKeys(fileSet)
	sort.Strings(files)

	return &Result{
		Graph: sub,
		Nodes: nodes,
		Edges: sub.Edges(),
		Files: files,
	}
}

// QueryFile returns the file node, all symbols defined in it, and files that
// import or are imported by it.
func QueryFile(g *graph.Graph, filePath string) *Result {
	ids := make(map[string]bool)
	fileSet := make(map[string]bool)

	// Find the file node and all children (symbols defined in this file)
	fileID := graph.FileNodeID(filePath)
	if n := g.Node(fileID); n != nil {
		ids[fileID] = true
		fileSet[filePath] = true
	}

	for _, n := range g.Nodes() {
		if n.FilePath == filePath {
			ids[n.ID] = true
		}
	}

	// Find files this file imports (outgoing import edges)
	for _, e := range g.EdgesFrom(fileID) {
		if e.Type == graph.EdgeImports {
			ids[e.ToID] = true
			if n := g.Node(e.ToID); n != nil && n.FilePath != "" {
				fileSet[n.FilePath] = true
			}
		}
	}

	// Find files that import this file (incoming import edges)
	for _, e := range g.EdgesTo(fileID) {
		if e.Type == graph.EdgeImports {
			ids[e.FromID] = true
			if n := g.Node(e.FromID); n != nil && n.FilePath != "" {
				fileSet[n.FilePath] = true
			}
		}
	}

	nodeIDs := mapKeys(ids)
	sub := g.Subgraph(nodeIDs)
	files := mapKeys(fileSet)
	sort.Strings(files)

	return &Result{
		Graph: sub,
		Nodes: sub.Nodes(),
		Edges: sub.Edges(),
		Files: files,
	}
}

// FormatForPrompt formats a Result as concise indented text suitable for LLM
// prompt injection, grouping nodes by file.
func FormatForPrompt(result *Result) string {
	if result == nil || len(result.Nodes) == 0 {
		return ""
	}

	// Group nodes by file path
	type fileInfo struct {
		imports    []string
		importedBy []string
		symbols    []*graph.Node
	}
	fileMap := make(map[string]*fileInfo)

	// Ensure all files have entries
	for _, f := range result.Files {
		if _, ok := fileMap[f]; !ok {
			fileMap[f] = &fileInfo{}
		}
	}

	// Collect import relationships from edges
	for _, e := range result.Edges {
		if e.Type != graph.EdgeImports {
			continue
		}
		var fromFile, toFile string
		for _, n := range result.Nodes {
			if n.ID == e.FromID && n.Type == graph.NodeFile {
				fromFile = n.FilePath
			}
			if n.ID == e.ToID && n.Type == graph.NodeFile {
				toFile = n.FilePath
			}
		}
		if fromFile != "" && toFile != "" {
			if fi, ok := fileMap[fromFile]; ok {
				fi.imports = appendUnique(fi.imports, toFile)
			}
			if fi, ok := fileMap[toFile]; ok {
				fi.importedBy = appendUnique(fi.importedBy, fromFile)
			}
		}
	}

	// Collect symbols per file
	for _, n := range result.Nodes {
		if n.Type == graph.NodeFile || n.Type == graph.NodeDir || n.Type == graph.NodeRepo {
			continue
		}
		if n.FilePath == "" {
			continue
		}
		fi, ok := fileMap[n.FilePath]
		if !ok {
			fi = &fileInfo{}
			fileMap[n.FilePath] = fi
		}
		fi.symbols = append(fi.symbols, n)
	}

	// Sort files
	sortedFiles := make([]string, 0, len(fileMap))
	for f := range fileMap {
		sortedFiles = append(sortedFiles, f)
	}
	sort.Strings(sortedFiles)

	var b strings.Builder
	for i, f := range sortedFiles {
		fi := fileMap[f]
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "── %s\n", f)

		if len(fi.imports) > 0 {
			sort.Strings(fi.imports)
			fmt.Fprintf(&b, "   imports: %s\n", strings.Join(fi.imports, ", "))
		}
		if len(fi.importedBy) > 0 {
			sort.Strings(fi.importedBy)
			fmt.Fprintf(&b, "   imported by: %s\n", strings.Join(fi.importedBy, ", "))
		}

		// Sort symbols by line number, then by name
		sort.Slice(fi.symbols, func(a, c int) bool {
			if fi.symbols[a].Line != fi.symbols[c].Line {
				return fi.symbols[a].Line < fi.symbols[c].Line
			}
			return fi.symbols[a].Name < fi.symbols[c].Name
		})

		for _, sym := range fi.symbols {
			prefix := symbolPrefix(sym.Type)
			fmt.Fprintf(&b, "   %s %s\n", prefix, sym.Name)
		}
	}

	return b.String()
}

func symbolPrefix(t graph.NodeType) string {
	switch t {
	case graph.NodeFunction:
		return "fn"
	case graph.NodeClass:
		return "class"
	case graph.NodeMethod:
		return "method"
	case graph.NodeVariable:
		return "var"
	case graph.NodeAttribute:
		return "attr"
	default:
		return string(t)
	}
}

func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
