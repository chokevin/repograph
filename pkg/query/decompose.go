package query

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chokevin/repograph/pkg/graph"
)

// DecomposeResult holds structured analysis of a single file's responsibilities,
// dependencies, and actionable extraction suggestions.
type DecomposeResult struct {
	File         string            `json:"file"`
	Language     string            `json:"language"`
	SymbolCount  int               `json:"symbol_count"`
	ExportCount  int               `json:"export_count"`
	Clusters     []*Cluster        `json:"clusters"`
	Importers    []*ImporterInfo   `json:"importers"`
	Dependencies []*DependencyInfo `json:"dependencies"`
	Suggestions  []*Suggestion     `json:"suggestions,omitempty"`
}

// Cluster is a group of symbols in the target file connected by intra-file calls.
type Cluster struct {
	Label         string          `json:"label"`
	Symbols       []string        `json:"symbols"`
	LineStart     int             `json:"line_start"`
	LineEnd       int             `json:"line_end"`
	ExternalCalls []*ExternalCall `json:"external_calls,omitempty"`
}

// ExternalCall records calls from a cluster to symbols in another file.
type ExternalCall struct {
	File    string   `json:"file"`
	Symbols []string `json:"symbols"`
}

// ImporterInfo records which symbols another file uses from the target file.
type ImporterInfo struct {
	File string   `json:"file"`
	Uses []string `json:"uses"`
}

// DependencyInfo records which symbols the target file uses from another file.
type DependencyInfo struct {
	File    string   `json:"file"`
	Symbols []string `json:"symbols"`
}

// Suggestion is an actionable extraction recommendation.
type Suggestion struct {
	Action          string   `json:"action"`
	Label           string   `json:"label"`
	TargetFile      string   `json:"target_file"`
	MoveSymbols     []string `json:"move_symbols"`
	UpdateImporters []string `json:"update_importers,omitempty"`
	AddImports      []string `json:"add_imports,omitempty"`
	Notes           []string `json:"notes,omitempty"`
}

// QueryDecompose analyzes a target file and produces concern clusters,
// dependency mapping, and actionable extraction suggestions.
func QueryDecompose(g *graph.Graph, filePath string) *DecomposeResult {
	fileID := graph.FileNodeID(filePath)
	fileNode := g.Node(fileID)
	if fileNode == nil {
		return nil
	}

	// Collect all symbols (functions, methods, classes, variables) in target file.
	var symbols []*graph.Node
	for _, n := range g.Nodes() {
		if n.FilePath == filePath && n.Type != graph.NodeFile && n.Type != graph.NodeDir && n.Type != graph.NodeRepo {
			symbols = append(symbols, n)
		}
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Line != symbols[j].Line {
			return symbols[i].Line < symbols[j].Line
		}
		return symbols[i].Name < symbols[j].Name
	})

	var exported []string
	for _, s := range symbols {
		if s.Exported {
			exported = append(exported, s.Name)
		}
	}

	// Build intra-file call graph (calls between symbols in same file).
	callableSymbols := filterCallable(symbols)
	symIDSet := make(map[string]bool)
	for _, s := range callableSymbols {
		symIDSet[s.ID] = true
	}

	// adjacency: undirected connections between callable symbols in same file.
	adj := make(map[string]map[string]bool)
	for _, s := range callableSymbols {
		adj[s.ID] = make(map[string]bool)
	}
	for _, s := range callableSymbols {
		for _, e := range g.EdgesFrom(s.ID) {
			if e.Type == graph.EdgeCalls && symIDSet[e.ToID] {
				adj[s.ID][e.ToID] = true
				adj[e.ToID][s.ID] = true
			}
		}
	}

	// Methods on the same class belong together: connect via Parent field or EdgeMethodOf.
	classMembers := make(map[string][]string) // class node ID → method node IDs
	for _, s := range callableSymbols {
		if s.Type == graph.NodeMethod && s.Parent != "" {
			classMembers[s.Parent] = append(classMembers[s.Parent], s.ID)
		}
		for _, e := range g.EdgesFrom(s.ID) {
			if e.Type == graph.EdgeMethodOf {
				classMembers[e.ToID] = append(classMembers[e.ToID], s.ID)
			}
		}
	}
	for _, members := range classMembers {
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				if adj[members[i]] != nil && adj[members[j]] != nil {
					adj[members[i]][members[j]] = true
					adj[members[j]][members[i]] = true
				}
			}
		}
	}

	// Find connected components via BFS.
	visited := make(map[string]bool)
	var components [][]*graph.Node
	for _, s := range callableSymbols {
		if visited[s.ID] {
			continue
		}
		var comp []*graph.Node
		queue := []string{s.ID}
		visited[s.ID] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if n := g.Node(cur); n != nil {
				comp = append(comp, n)
			}
			for neighbor := range adj[cur] {
				if !visited[neighbor] {
					visited[neighbor] = true
					queue = append(queue, neighbor)
				}
			}
		}
		sort.Slice(comp, func(i, j int) bool { return comp[i].Line < comp[j].Line })
		components = append(components, comp)
	}

	// Separate non-callable symbols into class-like types vs other.
	nonCallable := filterNonCallable(symbols)
	var classNodes []*graph.Node
	var otherNonCallable []*graph.Node
	for _, n := range nonCallable {
		if n.Type == graph.NodeClass {
			classNodes = append(classNodes, n)
		} else {
			otherNonCallable = append(otherNonCallable, n)
		}
	}

	// Build clusters.
	clusters := make([]*Cluster, 0, len(components))
	for _, comp := range components {
		c := buildCluster(comp, g, filePath)
		clusters = append(clusters, c)
	}

	// Attach class nodes to their method cluster via EdgeMethodOf relationships.
	attachClassNodes(clusters, classNodes, components, g)

	// Attach remaining orphan non-callable symbols to the nearest cluster.
	attachOrphans(clusters, otherNonCallable)

	// Merge singleton callable clusters into a [helpers] group to reduce noise.
	clusters = mergeSingletons(clusters)

	// Sort clusters by line start.
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].LineStart < clusters[j].LineStart
	})

	// Analyze importers: who imports this file and which symbols they call.
	importers := analyzeImporters(g, filePath, symbols)

	// Analyze dependencies: what this file imports and which symbols it uses.
	deps := analyzeDependencies(g, filePath, symbols)

	result := &DecomposeResult{
		File:         filePath,
		Language:     fileNode.Language,
		SymbolCount:  len(symbols),
		ExportCount:  len(exported),
		Clusters:     clusters,
		Importers:    importers,
		Dependencies: deps,
	}

	// Generate extraction suggestions for files with multiple loosely-coupled clusters.
	result.Suggestions = generateSuggestions(result, g, filePath, clusters, importers)

	return result
}

// buildCluster creates a Cluster from a connected component.
func buildCluster(comp []*graph.Node, g *graph.Graph, filePath string) *Cluster {
	names := make([]string, len(comp))
	lineStart, lineEnd := comp[0].Line, comp[0].Line
	for i, n := range comp {
		names[i] = n.Name
		if n.Line < lineStart {
			lineStart = n.Line
		}
		end := n.EndLine
		if end == 0 {
			end = n.Line
		}
		if end > lineEnd {
			lineEnd = end
		}
	}

	// Determine label: if all methods share a parent class, use the class name.
	// Otherwise use first exported name, or first name.
	label := ""
	commonParent := ""
	allSameParent := true
	for _, n := range comp {
		if n.Type == graph.NodeMethod && n.Parent != "" {
			if commonParent == "" {
				commonParent = n.Parent
			} else if commonParent != n.Parent {
				allSameParent = false
			}
		}
	}
	if allSameParent && commonParent != "" {
		// Extract class name from parent ID like "class:path:ClassName"
		parts := strings.Split(commonParent, ":")
		label = parts[len(parts)-1]
	}
	if label == "" {
		label = names[0]
		for _, n := range comp {
			if n.Exported {
				label = n.Name
				break
			}
		}
	}

	// Find external calls from this cluster.
	extCalls := findExternalCalls(comp, g, filePath)

	return &Cluster{
		Label:         label,
		Symbols:       names,
		LineStart:     lineStart,
		LineEnd:       lineEnd,
		ExternalCalls: extCalls,
	}
}

// findExternalCalls finds calls from cluster symbols to symbols in other files.
func findExternalCalls(comp []*graph.Node, g *graph.Graph, filePath string) []*ExternalCall {
	// file → set of symbol names
	byFile := make(map[string]map[string]bool)
	for _, n := range comp {
		for _, e := range g.EdgesFrom(n.ID) {
			if e.Type != graph.EdgeCalls && e.Type != graph.EdgeReferences {
				continue
			}
			target := g.Node(e.ToID)
			if target == nil || target.FilePath == filePath || target.FilePath == "" {
				continue
			}
			if byFile[target.FilePath] == nil {
				byFile[target.FilePath] = make(map[string]bool)
			}
			byFile[target.FilePath][target.Name] = true
		}
	}

	var result []*ExternalCall
	for f, syms := range byFile {
		result = append(result, &ExternalCall{
			File:    f,
			Symbols: sortedKeys(syms),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result
}

// analyzeImporters finds files that import the target and which symbols they use.
func analyzeImporters(g *graph.Graph, filePath string, symbols []*graph.Node) []*ImporterInfo {
	fileID := graph.FileNodeID(filePath)
	symIDSet := make(map[string]bool)
	symNameByID := make(map[string]string)
	for _, s := range symbols {
		symIDSet[s.ID] = true
		symNameByID[s.ID] = s.Name
	}

	// Files that import us.
	importerFiles := make(map[string]bool)
	for _, e := range g.EdgesTo(fileID) {
		if e.Type == graph.EdgeImports {
			if n := g.Node(e.FromID); n != nil && n.FilePath != "" {
				importerFiles[n.FilePath] = true
			}
		}
	}

	// For each importer file, find which of our symbols they call/reference.
	type importerData struct {
		file string
		uses map[string]bool
	}
	importerMap := make(map[string]*importerData)

	for importerFile := range importerFiles {
		// Find all symbols in the importer file.
		for _, n := range g.Nodes() {
			if n.FilePath != importerFile {
				continue
			}
			for _, e := range g.EdgesFrom(n.ID) {
				if (e.Type == graph.EdgeCalls || e.Type == graph.EdgeReferences) && symIDSet[e.ToID] {
					if importerMap[importerFile] == nil {
						importerMap[importerFile] = &importerData{file: importerFile, uses: make(map[string]bool)}
					}
					importerMap[importerFile].uses[symNameByID[e.ToID]] = true
				}
			}
		}
		// If we know they import us but can't determine specific symbols, still list them.
		if importerMap[importerFile] == nil {
			importerMap[importerFile] = &importerData{file: importerFile, uses: make(map[string]bool)}
		}
	}

	var result []*ImporterInfo
	for _, d := range importerMap {
		uses := sortedKeys(d.uses)
		result = append(result, &ImporterInfo{File: d.file, Uses: uses})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result
}

// analyzeDependencies finds what the target file imports and which symbols it uses.
func analyzeDependencies(g *graph.Graph, filePath string, symbols []*graph.Node) []*DependencyInfo {
	fileID := graph.FileNodeID(filePath)

	// Files we import.
	depFiles := make(map[string]bool)
	for _, e := range g.EdgesFrom(fileID) {
		if e.Type == graph.EdgeImports {
			if n := g.Node(e.ToID); n != nil && n.FilePath != "" {
				depFiles[n.FilePath] = true
			}
		}
	}

	// For each dependency, find which of its symbols we call/reference.
	depMap := make(map[string]map[string]bool)
	for _, s := range symbols {
		for _, e := range g.EdgesFrom(s.ID) {
			if e.Type != graph.EdgeCalls && e.Type != graph.EdgeReferences {
				continue
			}
			target := g.Node(e.ToID)
			if target == nil || target.FilePath == filePath || target.FilePath == "" {
				continue
			}
			if !depFiles[target.FilePath] {
				continue
			}
			if depMap[target.FilePath] == nil {
				depMap[target.FilePath] = make(map[string]bool)
			}
			depMap[target.FilePath][target.Name] = true
		}
	}

	// Include dep files even if we can't resolve specific symbols.
	for f := range depFiles {
		if depMap[f] == nil {
			depMap[f] = make(map[string]bool)
		}
	}

	var result []*DependencyInfo
	for f, syms := range depMap {
		result = append(result, &DependencyInfo{File: f, Symbols: sortedKeys(syms)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result
}

// generateSuggestions produces extraction recommendations when a file has
// multiple loosely-coupled concern clusters.
func generateSuggestions(dr *DecomposeResult, g *graph.Graph, filePath string, clusters []*Cluster, importers []*ImporterInfo) []*Suggestion {
	// Count total callable symbols to determine if the file is large enough
	// to warrant extraction suggestions.
	totalSymbols := 0
	for _, c := range clusters {
		totalSymbols += len(c.Symbols)
	}

	// Only suggest for files with enough symbols to be genuine monoliths.
	if totalSymbols < 10 {
		return nil
	}

	// Find clusters with 3+ symbols that aren't infrastructure/helpers and
	// don't dominate the file (>70% of symbols).
	var extractable []*Cluster
	for _, c := range clusters {
		if len(c.Symbols) < 3 {
			continue
		}
		if c.Label == "helpers" {
			continue
		}
		if totalSymbols > 0 && float64(len(c.Symbols))/float64(totalSymbols) > 0.7 {
			continue
		}
		extractable = append(extractable, c)
	}
	if len(extractable) < 2 {
		return nil
	}

	// Build a lookup: symbol name → which importers use it.
	symImporters := make(map[string][]string)
	for _, imp := range importers {
		for _, u := range imp.Uses {
			symImporters[u] = append(symImporters[u], imp.File)
		}
	}

	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameNoExt := strings.TrimSuffix(base, ext)

	var suggestions []*Suggestion
	for _, c := range extractable {

		targetName := fmt.Sprintf("%s/%s-%s%s", dir, nameNoExt, slugify(c.Label), ext)
		if dir == "." {
			targetName = fmt.Sprintf("%s-%s%s", nameNoExt, slugify(c.Label), ext)
		}

		// Which importers need updating for this cluster's symbols?
		affectedImporters := make(map[string]bool)
		for _, sym := range c.Symbols {
			for _, imp := range symImporters[sym] {
				affectedImporters[imp] = true
			}
		}

		// Which external deps does this cluster need?
		var addImports []string
		for _, ec := range c.ExternalCalls {
			addImports = append(addImports, fmt.Sprintf("%s: %s", ec.File, strings.Join(ec.Symbols, ", ")))
		}

		// Check cross-cluster dependencies: does another cluster call into this one?
		var notes []string
		for _, other := range clusters {
			if other.Label == c.Label {
				continue
			}
			// Check if other cluster's symbols call any of this cluster's symbols.
			crossCalls := findCrossClusterCalls(other, c, g)
			if len(crossCalls) > 0 {
				notes = append(notes, fmt.Sprintf("[%s] calls %s — add cross-import after extraction",
					other.Label, strings.Join(crossCalls, ", ")))
			}
		}

		suggestions = append(suggestions, &Suggestion{
			Action:          "extract",
			Label:           c.Label,
			TargetFile:      targetName,
			MoveSymbols:     c.Symbols,
			UpdateImporters: sortedKeys(affectedImporters),
			AddImports:      addImports,
			Notes:           notes,
		})
	}

	return suggestions
}

// findCrossClusterCalls finds symbols in 'from' cluster that call symbols in 'to' cluster.
func findCrossClusterCalls(from, to *Cluster, g *graph.Graph) []string {
	toSymSet := make(map[string]bool)
	for _, s := range to.Symbols {
		toSymSet[s] = true
	}

	var calls []string
	seen := make(map[string]bool)
	for _, fromSym := range from.Symbols {
		// Search all edges from nodes matching this symbol name.
		for _, n := range g.Nodes() {
			if n.Name != fromSym {
				continue
			}
			for _, e := range g.EdgesFrom(n.ID) {
				if e.Type != graph.EdgeCalls {
					continue
				}
				target := g.Node(e.ToID)
				if target != nil && toSymSet[target.Name] && !seen[target.Name] {
					seen[target.Name] = true
					calls = append(calls, target.Name)
				}
			}
		}
	}
	sort.Strings(calls)
	return calls
}

// FormatDecompose formats a DecomposeResult as concise, actionable text for LLM prompts.
func FormatDecompose(dr *DecomposeResult) string {
	if dr == nil {
		return ""
	}

	var b strings.Builder

	// Header.
	fmt.Fprintf(&b, "=== DECOMPOSE: %s ===\n", dr.File)
	fmt.Fprintf(&b, "%d symbols (%d exported)", dr.SymbolCount, dr.ExportCount)
	if dr.Language != "" {
		fmt.Fprintf(&b, ", %s", dr.Language)
	}
	b.WriteByte('\n')

	// Clusters.
	if len(dr.Clusters) > 0 {
		b.WriteString("\nCLUSTERS:\n")
		for _, c := range dr.Clusters {
			fmt.Fprintf(&b, "  [%s] %s", c.Label, strings.Join(c.Symbols, ", "))
			if c.LineStart > 0 {
				fmt.Fprintf(&b, " (lines %d–%d)", c.LineStart, c.LineEnd)
			}
			b.WriteByte('\n')
			for _, ec := range c.ExternalCalls {
				shortFile := filepath.Base(ec.File)
				fmt.Fprintf(&b, "    calls → %s: %s\n", shortFile, strings.Join(ec.Symbols, ", "))
			}
		}
	}

	// Importers.
	if len(dr.Importers) > 0 {
		b.WriteString("\nIMPORTERS:\n")
		for _, imp := range dr.Importers {
			if len(imp.Uses) > 0 {
				fmt.Fprintf(&b, "  %s uses: %s\n", imp.File, strings.Join(imp.Uses, ", "))
			} else {
				fmt.Fprintf(&b, "  %s\n", imp.File)
			}
		}
	}

	// Dependencies.
	if len(dr.Dependencies) > 0 {
		b.WriteString("\nDEPENDENCIES:\n")
		for _, dep := range dr.Dependencies {
			if len(dep.Symbols) > 0 {
				fmt.Fprintf(&b, "  %s: %s\n", dep.File, strings.Join(dep.Symbols, ", "))
			} else {
				fmt.Fprintf(&b, "  %s\n", dep.File)
			}
		}
	}

	// Suggestions.
	if len(dr.Suggestions) > 0 {
		b.WriteString("\nSUGGESTIONS:\n")
		for _, s := range dr.Suggestions {
			fmt.Fprintf(&b, "  Extract [%s] → %s\n", s.Label, s.TargetFile)
			fmt.Fprintf(&b, "    Move: %s\n", strings.Join(s.MoveSymbols, ", "))
			if len(s.UpdateImporters) > 0 {
				fmt.Fprintf(&b, "    Update importers: %s\n", strings.Join(s.UpdateImporters, ", "))
			}
			if len(s.AddImports) > 0 {
				for _, imp := range s.AddImports {
					fmt.Fprintf(&b, "    Import: %s\n", imp)
				}
			}
			for _, note := range s.Notes {
				fmt.Fprintf(&b, "    Note: %s\n", note)
			}
		}
	}

	return b.String()
}

// filterCallable returns only function and method nodes.
func filterCallable(nodes []*graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeFunction || n.Type == graph.NodeMethod {
			out = append(out, n)
		}
	}
	return out
}


// mergeSingletons collapses singleton clusters (1 callable symbol, no attached types)
// into a single [helpers] cluster to reduce noise in the output.
// Exported singletons are kept separate since they represent public API surface.
func mergeSingletons(clusters []*Cluster) []*Cluster {
	var merged []*Cluster
	var singletons []*Cluster
	for _, c := range clusters {
		isExportedSingleton := len(c.Symbols) == 1 && c.Label[0] >= 'A' && c.Label[0] <= 'Z'
		if len(c.Symbols) == 1 && len(c.ExternalCalls) == 0 && !isExportedSingleton {
			singletons = append(singletons, c)
		} else {
			merged = append(merged, c)
		}
	}
	if len(singletons) < 2 {
		// Not worth merging 0 or 1 singleton.
		return clusters
	}
	// Build helpers cluster.
	helpers := &Cluster{Label: "helpers"}
	for _, s := range singletons {
		helpers.Symbols = append(helpers.Symbols, s.Symbols...)
		if helpers.LineStart == 0 || s.LineStart < helpers.LineStart {
			helpers.LineStart = s.LineStart
		}
		if s.LineEnd > helpers.LineEnd {
			helpers.LineEnd = s.LineEnd
		}
	}
	merged = append(merged, helpers)
	return merged
}

// filterNonCallable returns nodes that are not functions or methods.
func filterNonCallable(nodes []*graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, n := range nodes {
		if n.Type != graph.NodeFunction && n.Type != graph.NodeMethod {
			out = append(out, n)
		}
	}
	return out
}

// attachClassNodes assigns class/struct nodes to the cluster that contains their methods.
func attachClassNodes(clusters []*Cluster, classNodes []*graph.Node, components [][]*graph.Node, g *graph.Graph) {
	if len(clusters) == 0 || len(classNodes) == 0 {
		return
	}
	for _, cn := range classNodes {
		attached := false
		// Find which cluster has methods of this class.
		for ci, comp := range components {
			if ci >= len(clusters) {
				break
			}
			for _, m := range comp {
				for _, e := range g.EdgesFrom(m.ID) {
					if e.Type == graph.EdgeMethodOf && e.ToID == cn.ID {
						clusters[ci].Symbols = append(clusters[ci].Symbols, cn.Name)
						if cn.Line < clusters[ci].LineStart {
							clusters[ci].LineStart = cn.Line
						}
						end := cn.EndLine
						if end == 0 {
							end = cn.Line
						}
						if end > clusters[ci].LineEnd {
							clusters[ci].LineEnd = end
						}
						attached = true
						break
					}
				}
				if attached {
					break
				}
			}
			if attached {
				break
			}
		}
		// Fallback: attach to nearest cluster by line.
		if !attached {
			attachOrphans(clusters, []*graph.Node{cn})
		}
	}
}

// attachOrphans assigns non-callable symbols to the nearest cluster by line number.
func attachOrphans(clusters []*Cluster, orphans []*graph.Node) {
	if len(clusters) == 0 || len(orphans) == 0 {
		return
	}
	for _, o := range orphans {
		best := 0
		bestDist := abs(o.Line - clusters[0].LineStart)
		for i := 1; i < len(clusters); i++ {
			d := abs(o.Line - clusters[i].LineStart)
			if d < bestDist {
				bestDist = d
				best = i
			}
		}
		clusters[best].Symbols = append(clusters[best].Symbols, o.Name)
		if o.Line < clusters[best].LineStart {
			clusters[best].LineStart = o.Line
		}
		end := o.EndLine
		if end == 0 {
			end = o.Line
		}
		if end > clusters[best].LineEnd {
			clusters[best].LineEnd = end
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	return strings.Trim(s, "-")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
