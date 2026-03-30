package graph

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// NodeType identifies the kind of graph node.
type NodeType string

const (
	NodeRepo      NodeType = "repo"
	NodeDir       NodeType = "dir"
	NodeFile      NodeType = "file"
	NodeClass     NodeType = "class"
	NodeFunction  NodeType = "function"
	NodeMethod    NodeType = "method"
	NodeVariable  NodeType = "variable"
	NodeAttribute NodeType = "attribute"
)

// EdgeType identifies the kind of relationship between nodes.
type EdgeType string

const (
	EdgeContains   EdgeType = "contains"
	EdgeImports    EdgeType = "imports"
	EdgeCalls      EdgeType = "calls"
	EdgeReferences EdgeType = "references"
	EdgeInherits   EdgeType = "inherits"
	EdgeDefines    EdgeType = "defines"
	EdgeMethodOf   EdgeType = "method_of"
)

// Node represents a single entity in the code graph.
type Node struct {
	ID       string   `json:"id"`
	Type     NodeType `json:"type"`
	Name     string   `json:"name"`
	FilePath string   `json:"file_path,omitempty"`
	Language string   `json:"language,omitempty"`
	Exported bool     `json:"exported,omitempty"`
	Line     int      `json:"line,omitempty"`
	EndLine  int      `json:"end_line,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Edge represents a relationship between two nodes.
type Edge struct {
	FromID string   `json:"from_id"`
	ToID   string   `json:"to_id"`
	Type   EdgeType `json:"type"`
	Weight float64  `json:"weight,omitempty"`
}

// Graph is a thread-safe directed graph of code entities.
type Graph struct {
	mu     sync.RWMutex
	nodes  map[string]*Node
	adj    map[string][]*Edge
	revAdj map[string][]*Edge
}

// New creates an empty graph.
func New() *Graph {
	return &Graph{
		nodes:  make(map[string]*Node),
		adj:    make(map[string][]*Edge),
		revAdj: make(map[string][]*Edge),
	}
}

// AddNode adds a node to the graph. Returns false if the ID already exists.
func (g *Graph) AddNode(n *Node) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.nodes[n.ID]; exists {
		return false
	}
	g.nodes[n.ID] = n
	return true
}

// AddEdge adds a directed edge. Both endpoints must exist.
func (g *Graph) AddEdge(e *Edge) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[e.FromID]; !ok {
		return false
	}
	if _, ok := g.nodes[e.ToID]; !ok {
		return false
	}
	g.adj[e.FromID] = append(g.adj[e.FromID], e)
	g.revAdj[e.ToID] = append(g.revAdj[e.ToID], e)
	return true
}

// RemoveEdges removes the given edges from the graph by pointer identity.
func (g *Graph) RemoveEdges(edges []*Edge) {
	if len(edges) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	remove := make(map[*Edge]bool, len(edges))
	for _, e := range edges {
		remove[e] = true
	}
	for from, list := range g.adj {
		filtered := list[:0]
		for _, e := range list {
			if !remove[e] {
				filtered = append(filtered, e)
			}
		}
		g.adj[from] = filtered
	}
	for to, list := range g.revAdj {
		filtered := list[:0]
		for _, e := range list {
			if !remove[e] {
				filtered = append(filtered, e)
			}
		}
		g.revAdj[to] = filtered
	}
}

// Node returns a node by ID, or nil if not found.
func (g *Graph) Node(id string) *Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.nodes[id]
}

// Nodes returns all nodes.
func (g *Graph) Nodes() []*Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	return out
}

// NodesByType returns all nodes of a given type.
func (g *Graph) NodesByType(t NodeType) []*Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []*Node
	for _, n := range g.nodes {
		if n.Type == t {
			out = append(out, n)
		}
	}
	return out
}

// Edges returns all edges.
func (g *Graph) Edges() []*Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []*Edge
	for _, edges := range g.adj {
		out = append(out, edges...)
	}
	return out
}

// EdgesFrom returns outgoing edges from a node.
func (g *Graph) EdgesFrom(id string) []*Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.adj[id]
}

// EdgesTo returns incoming edges to a node.
func (g *Graph) EdgesTo(id string) []*Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.revAdj[id]
}

// NodeCount returns the number of nodes.
func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// EdgeCount returns the number of edges.
func (g *Graph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	count := 0
	for _, edges := range g.adj {
		count += len(edges)
	}
	return count
}

// FileNodes returns all file-type nodes.
func (g *Graph) FileNodes() []*Node {
	return g.NodesByType(NodeFile)
}

// Languages returns the set of distinct languages found in file nodes.
func (g *Graph) Languages() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	seen := make(map[string]bool)
	for _, n := range g.nodes {
		if n.Type == NodeFile && n.Language != "" {
			seen[n.Language] = true
		}
	}
	out := make([]string, 0, len(seen))
	for lang := range seen {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// RelatedFiles finds files reachable from filePath within depth hops via any edge.
func (g *Graph) RelatedFiles(filePath string, depth int) []*Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if depth <= 0 {
		depth = 2
	}

	startID := FileNodeID(filePath)
	visited := map[string]bool{startID: true}

	// Collect all node IDs belonging to the start file
	frontier := []string{startID}
	for _, n := range g.nodes {
		if n.FilePath == filePath && n.ID != startID {
			frontier = append(frontier, n.ID)
			visited[n.ID] = true
		}
	}

	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, nid := range frontier {
			for _, e := range g.adj[nid] {
				targetFile := g.fileAncestor(e.ToID)
				if targetFile != "" && !visited[targetFile] {
					visited[targetFile] = true
					next = append(next, targetFile)
				}
			}
			for _, e := range g.revAdj[nid] {
				targetFile := g.fileAncestor(e.FromID)
				if targetFile != "" && !visited[targetFile] {
					visited[targetFile] = true
					next = append(next, targetFile)
				}
			}
		}
		frontier = next
	}

	delete(visited, startID)
	var result []*Node
	for id := range visited {
		if n := g.nodes[id]; n != nil && n.Type == NodeFile {
			result = append(result, n)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].FilePath < result[j].FilePath })
	return result
}

// Subgraph extracts a new graph containing only the specified node IDs and edges between them.
func (g *Graph) Subgraph(nodeIDs []string) *Graph {
	g.mu.RLock()
	defer g.mu.RUnlock()

	sub := New()
	keep := make(map[string]bool)
	for _, id := range nodeIDs {
		keep[id] = true
		if n := g.nodes[id]; n != nil {
			sub.nodes[n.ID] = n
		}
	}
	for _, id := range nodeIDs {
		for _, e := range g.adj[id] {
			if keep[e.ToID] {
				sub.adj[e.FromID] = append(sub.adj[e.FromID], e)
				sub.revAdj[e.ToID] = append(sub.revAdj[e.ToID], e)
			}
		}
	}
	return sub
}

// Search finds nodes whose Name contains the query substring (case-insensitive).
func (g *Graph) Search(query string) []*Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := strings.ToLower(query)
	var results []*Node
	for _, n := range g.nodes {
		if strings.Contains(strings.ToLower(n.Name), q) || strings.Contains(strings.ToLower(n.FilePath), q) {
			results = append(results, n)
		}
	}
	return results
}

// Summary returns a human-readable / LLM-friendly summary of the graph.
func (g *Graph) Summary() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	files := g.NodesByType(NodeFile)
	funcs := g.NodesByType(NodeFunction)
	classes := g.NodesByType(NodeClass)
	methods := g.NodesByType(NodeMethod)
	vars := g.NodesByType(NodeVariable)

	var b strings.Builder
	b.WriteString("Repository Graph: ")
	b.WriteString(itoa(len(files)) + " files, ")
	b.WriteString(itoa(len(funcs)) + " functions, ")
	b.WriteString(itoa(len(classes)) + " classes, ")
	b.WriteString(itoa(len(methods)) + " methods, ")
	b.WriteString(itoa(len(vars)) + " variables, ")
	b.WriteString(itoa(g.EdgeCount()) + " edges\n")
	b.WriteString("Languages: " + strings.Join(g.Languages(), ", ") + "\n")

	sort.Slice(files, func(i, j int) bool { return files[i].FilePath < files[j].FilePath })
	for _, f := range files {
		b.WriteString("\n─ " + f.FilePath)
		// Show imports
		var imports []string
		for _, e := range g.adj[f.ID] {
			if e.Type == EdgeImports {
				if target := g.nodes[e.ToID]; target != nil {
					imports = append(imports, target.FilePath)
				}
			}
		}
		if len(imports) > 0 {
			b.WriteString(" → imports: " + strings.Join(imports, ", "))
		}
		// Show exports
		var exports []string
		for _, n := range g.nodes {
			if n.FilePath == f.FilePath && n.Exported && n.Type != NodeFile {
				exports = append(exports, n.Name)
			}
		}
		if len(exports) > 0 {
			sort.Strings(exports)
			b.WriteString(" → exports: " + strings.Join(exports, ", "))
		}
	}
	return b.String()
}

// fileAncestor finds the file node that contains the given node ID.
func (g *Graph) fileAncestor(id string) string {
	n := g.nodes[id]
	if n == nil {
		return ""
	}
	if n.Type == NodeFile {
		return n.ID
	}
	// Look up by file path
	fileID := FileNodeID(n.FilePath)
	if _, ok := g.nodes[fileID]; ok {
		return fileID
	}
	return ""
}

// FileNodeID generates the canonical ID for a file node.
func FileNodeID(path string) string { return "file:" + path }

// DirNodeID generates the canonical ID for a directory node.
func DirNodeID(path string) string { return "dir:" + path }

// FuncNodeID generates the canonical ID for a function node.
func FuncNodeID(filePath, name string) string { return "func:" + filePath + ":" + name }

// ClassNodeID generates the canonical ID for a class node.
func ClassNodeID(filePath, name string) string { return "class:" + filePath + ":" + name }

// MethodNodeID generates the canonical ID for a method node.
func MethodNodeID(filePath, className, methodName string) string {
	return "method:" + filePath + ":" + className + "." + methodName
}

// VarNodeID generates the canonical ID for a variable node.
func VarNodeID(filePath, scope, name string) string {
	return "var:" + filePath + ":" + scope + "." + name
}

func itoa(n int) string { return strconv.Itoa(n) }
