package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chokevin/repograph/pkg/graph"
)

// ConstraintResult holds detected patterns and actionable constraints for a file.
type ConstraintResult struct {
	File        string        `json:"file"`
	Interfaces  []*Interface  `json:"interfaces,omitempty"`
	Implements  []*ImplInfo   `json:"implements,omitempty"`
	Pipelines   []*Pipeline   `json:"pipelines,omitempty"`
	Constraints []string      `json:"constraints,omitempty"`
}

// Interface is a detected interface type with its required method set.
type Interface struct {
	Name     string   `json:"name"`
	FilePath string   `json:"file_path"`
	Methods  []string `json:"methods"`
	Impls    []string `json:"implementations"`
}

// ImplInfo records that a struct implements an interface.
type ImplInfo struct {
	Struct    string `json:"struct"`
	FilePath  string `json:"file_path"`
	Interface string `json:"interface"`
	IntfFile  string `json:"interface_file"`
}

// Pipeline is an ordered sequence of similar calls within a function.
type Pipeline struct {
	Function string       `json:"function"`
	FilePath string       `json:"file_path"`
	Steps    []*PipeStep  `json:"steps"`
}

// PipeStep is one step in a detected pipeline.
type PipeStep struct {
	Call     string `json:"call"`
	Target   string `json:"target"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
}

// QueryConstraints detects patterns and generates actionable constraints for a file.
func QueryConstraints(g *graph.Graph, filePath string) *ConstraintResult {
	result := &ConstraintResult{File: filePath}

	// Detect all interfaces in the graph.
	allInterfaces := detectInterfaces(g)

	// Detect which structs in the target file implement interfaces.
	result.Implements = detectImplementations(g, allInterfaces, filePath)

	// Filter interfaces to only those relevant to the target file.
	result.Interfaces = filterRelevantInterfaces(allInterfaces, filePath, result.Implements)

	// Detect pipelines in functions of the target file.
	result.Pipelines = detectPipelines(g, filePath)

	// Generate human-readable constraints.
	result.Constraints = generateConstraints(result, g, filePath)

	return result
}

// detectInterfaces finds all Go interface types and their method sets.
func detectInterfaces(g *graph.Graph) []*Interface {
	var interfaces []*Interface

	for _, n := range g.NodesByType(graph.NodeClass) {
		if n.Metadata == nil || n.Metadata["kind"] != "interface" {
			continue
		}
		methodStr := n.Metadata["methods"]
		if methodStr == "" {
			continue
		}
		methods := strings.Split(methodStr, ",")
		sort.Strings(methods)

		iface := &Interface{
			Name:     n.Name,
			FilePath: n.FilePath,
			Methods:  methods,
		}

		// Find implementations: structs whose method names are a superset.
		iface.Impls = findImplementors(g, methods)
		if len(iface.Impls) > 0 {
			interfaces = append(interfaces, iface)
		}
	}

	sort.Slice(interfaces, func(i, j int) bool {
		return interfaces[i].Name < interfaces[j].Name
	})
	return interfaces
}

// filterRelevantInterfaces returns only interfaces that are:
// - defined in the target file, OR
// - implemented by a struct in the target file
func filterRelevantInterfaces(all []*Interface, filePath string, impls []*ImplInfo) []*Interface {
	implIfaces := make(map[string]bool)
	for _, impl := range impls {
		implIfaces[impl.Interface] = true
	}

	var relevant []*Interface
	for _, iface := range all {
		if iface.FilePath == filePath || implIfaces[iface.Name] {
			relevant = append(relevant, iface)
		}
	}
	return relevant
}

// findImplementors returns struct names whose methods are a superset of required.
func findImplementors(g *graph.Graph, required []string) []string {
	reqSet := make(map[string]bool)
	for _, m := range required {
		reqSet[m] = true
	}

	// Build struct → method names index.
	structMethods := make(map[string]map[string]bool) // classID → set of method names
	structName := make(map[string]string)              // classID → "Name (file)"
	for _, n := range g.NodesByType(graph.NodeMethod) {
		if n.Parent == "" {
			continue
		}
		parent := g.Node(n.Parent)
		if parent == nil || parent.Metadata == nil || parent.Metadata["kind"] == "interface" {
			continue
		}
		if structMethods[n.Parent] == nil {
			structMethods[n.Parent] = make(map[string]bool)
			structName[n.Parent] = parent.Name + " (" + parent.FilePath + ")"
		}
		structMethods[n.Parent][n.Name] = true
	}

	var impls []string
	for classID, methods := range structMethods {
		match := true
		for m := range reqSet {
			if !methods[m] {
				match = false
				break
			}
		}
		if match {
			impls = append(impls, structName[classID])
		}
	}
	sort.Strings(impls)
	return impls
}

// detectImplementations finds which structs in the target file implement which interfaces.
func detectImplementations(g *graph.Graph, interfaces []*Interface, filePath string) []*ImplInfo {
	// Collect structs in target file.
	var structs []*graph.Node
	for _, n := range g.NodesByType(graph.NodeClass) {
		if n.FilePath == filePath && n.Metadata != nil && n.Metadata["kind"] != "interface" {
			structs = append(structs, n)
		}
	}
	if len(structs) == 0 {
		return nil
	}

	// Build method set per struct.
	structMethods := make(map[string]map[string]bool)
	for _, n := range g.NodesByType(graph.NodeMethod) {
		if n.FilePath != filePath || n.Parent == "" {
			continue
		}
		if structMethods[n.Parent] == nil {
			structMethods[n.Parent] = make(map[string]bool)
		}
		structMethods[n.Parent][n.Name] = true
	}

	var impls []*ImplInfo
	for _, s := range structs {
		methods := structMethods[s.ID]
		if methods == nil {
			continue
		}
		for _, iface := range interfaces {
			match := true
			for _, m := range iface.Methods {
				if !methods[m] {
					match = false
					break
				}
			}
			if match {
				impls = append(impls, &ImplInfo{
					Struct:    s.Name,
					FilePath:  s.FilePath,
					Interface: iface.Name,
					IntfFile:  iface.FilePath,
				})
			}
		}
	}
	return impls
}

// detectPipelines finds ordered sequences of similar calls in functions of filePath.
func detectPipelines(g *graph.Graph, filePath string) []*Pipeline {
	// Build set of "ubiquitous" names: functions/methods whose name appears
	// in 4+ distinct parent classes. These are interface methods that get
	// false-matched by name-based resolution (e.g. String, Error, Run, Info).
	nameParents := make(map[string]map[string]bool) // name → set of parent class IDs
	for _, n := range g.Nodes() {
		if n.Type != graph.NodeFunction && n.Type != graph.NodeMethod {
			continue
		}
		parent := n.Parent
		if parent == "" {
			parent = "__freestanding__"
		}
		if nameParents[n.Name] == nil {
			nameParents[n.Name] = make(map[string]bool)
		}
		nameParents[n.Name][parent] = true
	}
	ubiquitous := make(map[string]bool)
	for name, parents := range nameParents {
		if len(parents) >= 4 {
			ubiquitous[name] = true
		}
	}

	var pipelines []*Pipeline

	var funcs []*graph.Node
	for _, n := range g.Nodes() {
		if n.FilePath == filePath && (n.Type == graph.NodeFunction || n.Type == graph.NodeMethod) {
			funcs = append(funcs, n)
		}
	}

	for _, fn := range funcs {
		pipeline := detectPipelineInFunc(g, fn, ubiquitous)
		if pipeline != nil {
			pipelines = append(pipelines, pipeline)
		}
	}

	return pipelines
}

// detectPipelineInFunc looks for ordered sequences of calls to functions with
// a shared naming pattern (e.g., all named Execute, NewAction, etc.)
func detectPipelineInFunc(g *graph.Graph, fn *graph.Node, ubiquitous map[string]bool) *Pipeline {

	type callInfo struct {
		name     string
		targetID string
		file     string
		pkg      string
		line     int
	}

	pkgOf := func(path string) string {
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return path
		}
		return strings.Join(parts[:len(parts)-1], "/")
	}

	// Collect all outgoing call edges from this function, with target info.
	var calls []callInfo
	for _, e := range g.EdgesFrom(fn.ID) {
		if e.Type != graph.EdgeCalls {
			continue
		}
		target := g.Node(e.ToID)
		if target == nil || target.FilePath == fn.FilePath {
			continue
		}
		if ubiquitous[target.Name] {
			continue
		}
		calls = append(calls, callInfo{
			name:     target.Name,
			targetID: target.ID,
			file:     target.FilePath,
			pkg:      pkgOf(target.FilePath),
			line:     target.Line,
		})
	}

	if len(calls) < 3 {
		return nil
	}

	// Group calls by name to find repeated patterns.
	nameCount := make(map[string][]callInfo)
	for _, c := range calls {
		nameCount[c.name] = append(nameCount[c.name], c)
	}

	// Find the most repeated external call name that goes to distinct packages.
	var bestName string
	var bestCount int
	for name, cs := range nameCount {
		pkgs := make(map[string]bool)
		for _, c := range cs {
			pkgs[c.pkg] = true
		}
		if len(pkgs) >= 3 && len(cs) > bestCount {
			bestName = name
			bestCount = len(cs)
		}
	}

	if bestName == "" || bestCount < 3 {
		return nil
	}

	// Build pipeline from the repeated calls, dedup by package.
	var steps []*PipeStep
	seen := make(map[string]bool)
	for _, c := range nameCount[bestName] {
		if seen[c.pkg] {
			continue
		}
		seen[c.pkg] = true
		steps = append(steps, &PipeStep{
			Call:     bestName,
			Target:   c.targetID,
			FilePath: c.file,
			Line:     c.line,
		})
	}

	if len(steps) < 3 {
		return nil
	}

	return &Pipeline{
		Function: fn.Name,
		FilePath: fn.FilePath,
		Steps:    steps,
	}
}

// generateConstraints produces human-readable constraint strings from detected patterns.
func generateConstraints(cr *ConstraintResult, g *graph.Graph, filePath string) []string {
	var constraints []string

	// Interface implementation constraints.
	for _, impl := range cr.Implements {
		iface := findIface(cr.Interfaces, impl.Interface)
		if iface != nil {
			constraints = append(constraints,
				fmt.Sprintf("%s implements %s (defined in %s). Required methods: %s",
					impl.Struct, impl.Interface, impl.IntfFile,
					strings.Join(iface.Methods, ", ")))
		}
	}

	// Interface constraints for interfaces defined in this file.
	for _, iface := range cr.Interfaces {
		if iface.FilePath != filePath {
			continue
		}
		constraints = append(constraints,
			fmt.Sprintf("Interface %s requires: %s. Implemented by: %s",
				iface.Name, strings.Join(iface.Methods, ", "),
				strings.Join(iface.Impls, "; ")))
	}

	// Pipeline constraints.
	for _, p := range cr.Pipelines {
		var stepDescs []string
		for i, s := range p.Steps {
			base := fileBase(s.FilePath)
			stepDescs = append(stepDescs, fmt.Sprintf("%d. %s (%s)", i+1, s.Call, base))
		}
		constraints = append(constraints,
			fmt.Sprintf("Function %s executes a %d-step pipeline calling %s: %s",
				p.Function, len(p.Steps), p.Steps[0].Call,
				strings.Join(stepDescs, " → ")))
	}

	return constraints
}

// FormatConstraints formats a ConstraintResult as actionable text.
func FormatConstraints(cr *ConstraintResult) string {
	if cr == nil {
		return ""
	}

	var b strings.Builder

	fmt.Fprintf(&b, "=== CONSTRAINTS: %s ===\n", cr.File)

	if len(cr.Interfaces) > 0 {
		b.WriteString("\nINTERFACES IN GRAPH:\n")
		for _, iface := range cr.Interfaces {
			fmt.Fprintf(&b, "  %s (%s)\n", iface.Name, iface.FilePath)
			fmt.Fprintf(&b, "    requires: %s\n", strings.Join(iface.Methods, ", "))
			if len(iface.Impls) > 0 {
				for _, impl := range iface.Impls {
					fmt.Fprintf(&b, "    implemented by: %s\n", impl)
				}
			}
		}
	}

	if len(cr.Implements) > 0 {
		b.WriteString("\nIMPLEMENTS:\n")
		for _, impl := range cr.Implements {
			fmt.Fprintf(&b, "  %s implements %s (from %s)\n",
				impl.Struct, impl.Interface, impl.IntfFile)
		}
	}

	if len(cr.Pipelines) > 0 {
		b.WriteString("\nPIPELINES:\n")
		for _, p := range cr.Pipelines {
			fmt.Fprintf(&b, "  %s() calls %s in sequence:\n", p.Function, p.Steps[0].Call)
			for i, s := range p.Steps {
				fmt.Fprintf(&b, "    %d. %s → %s\n", i+1, s.Call, fileBase(s.FilePath))
			}
		}
	}

	if len(cr.Constraints) > 0 {
		b.WriteString("\nACTIONABLE CONSTRAINTS:\n")
		for _, c := range cr.Constraints {
			fmt.Fprintf(&b, "  • %s\n", c)
		}
	}

	return b.String()
}

func findIface(interfaces []*Interface, name string) *Interface {
	for _, i := range interfaces {
		if i.Name == name {
			return i
		}
	}
	return nil
}

func fileBase(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return path
	}
	return strings.Join(parts[len(parts)-2:], "/")
}
