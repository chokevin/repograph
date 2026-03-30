package golang

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"github.com/chokevin/repograph/pkg/graph"
)

func init() {
	graph.RegisterPlugin(&Plugin{})
}

// Plugin implements graph.LanguagePlugin for Go.
type Plugin struct{}

func (p *Plugin) Name() string             { return "go" }
func (p *Plugin) Extensions() []string     { return []string{".go"} }
func (p *Plugin) Language() *sitter.Language { return golang.GetLanguage() }

func (p *Plugin) Parse(filePath string, source []byte, root *sitter.Node) *graph.ParseResult {
	pr := &graph.ParseResult{}
	fileID := graph.FileNodeID(filePath)

	graph.WalkTree(root, func(n *sitter.Node) {
		switch n.Type() {

		// ── Imports ──────────────────────────────────────────────────────
		case "import_spec":
			pathNode := graph.ChildByType(n, "interpreted_string_literal")
			if pathNode == nil {
				return
			}
			importPath := graph.Unquote(graph.NodeText(pathNode, source))
			if isStdlib(importPath) {
				return
			}
			pr.Edges = append(pr.Edges, &graph.Edge{
				FromID: fileID,
				ToID:   "unresolved-import:" + importPath,
				Type:   graph.EdgeImports,
			})

		// ── Function declarations ────────────────────────────────────────
		case "function_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return
			}
			name := graph.NodeText(nameNode, source)
			id := graph.FuncNodeID(filePath, name)
			pr.Nodes = append(pr.Nodes, &graph.Node{
				ID:       id,
				Type:     graph.NodeFunction,
				Name:     name,
				FilePath: filePath,
				Language: "go",
				Exported: graph.IsExported(name, "go"),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})

		// ── Method declarations ──────────────────────────────────────────
		case "method_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return
			}
			name := graph.NodeText(nameNode, source)
			receiver := extractReceiver(n, source)
			id := graph.MethodNodeID(filePath, receiver, name)
			pr.Nodes = append(pr.Nodes, &graph.Node{
				ID:       id,
				Type:     graph.NodeMethod,
				Name:     name,
				FilePath: filePath,
				Language: "go",
				Exported: graph.IsExported(name, "go"),
				Parent:   graph.ClassNodeID(filePath, receiver),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})
			if receiver != "" {
				pr.Edges = append(pr.Edges, &graph.Edge{
					FromID: id,
					ToID:   "unresolved:" + receiver,
					Type:   graph.EdgeMethodOf,
				})
			}

		// ── Type declarations (struct / interface) ───────────────────────
		case "type_declaration":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				spec := n.NamedChild(i)
				if spec.Type() != "type_spec" {
					continue
				}
				nameNode := spec.ChildByFieldName("name")
				if nameNode == nil {
					continue
				}
				name := graph.NodeText(nameNode, source)
				classID := graph.ClassNodeID(filePath, name)

				// Distinguish interface from struct.
				kind := "struct"
				if graph.ChildByType(spec, "interface_type") != nil {
					kind = "interface"
				}

				meta := map[string]string{"kind": kind}

				// For interfaces, extract method signatures.
				if kind == "interface" {
					ifaceNode := graph.ChildByType(spec, "interface_type")
					var methods []string
					for j := 0; j < int(ifaceNode.NamedChildCount()); j++ {
						child := ifaceNode.NamedChild(j)
						if child.Type() == "method_elem" {
							fieldID := graph.ChildByType(child, "field_identifier")
							if fieldID != nil {
								methods = append(methods, graph.NodeText(fieldID, source))
							}
						}
					}
					if len(methods) > 0 {
						meta["methods"] = strings.Join(methods, ",")
					}
				}

				pr.Nodes = append(pr.Nodes, &graph.Node{
					ID:       classID,
					Type:     graph.NodeClass,
					Name:     name,
					FilePath: filePath,
					Language: "go",
					Exported: graph.IsExported(name, "go"),
					Line:     int(spec.StartPoint().Row) + 1,
					EndLine:  int(spec.EndPoint().Row) + 1,
					Metadata: meta,
				})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: classID, Type: graph.EdgeDefines})
			}

		// ── Package-scope variables / constants ──────────────────────────
		case "var_declaration", "const_declaration":
			if !isPackageScope(n) {
				return
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				spec := n.NamedChild(i)
				if spec.Type() != "var_spec" && spec.Type() != "const_spec" {
					continue
				}
				nameNode := graph.ChildByType(spec, "identifier")
				if nameNode == nil {
					continue
				}
				name := graph.NodeText(nameNode, source)
				id := graph.VarNodeID(filePath, "package", name)
				pr.Nodes = append(pr.Nodes, &graph.Node{
					ID:       id,
					Type:     graph.NodeVariable,
					Name:     name,
					FilePath: filePath,
					Language: "go",
					Exported: graph.IsExported(name, "go"),
					Line:     int(spec.StartPoint().Row) + 1,
				})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})
			}

		// ── Call expressions ─────────────────────────────────────────────
		case "call_expression":
			name := goCallName(n, source)
			if name != "" {
				scope := findEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeCalls})
			}

		// ── Identifier references ────────────────────────────────────────
		case "identifier":
			if isGoReference(n) {
				name := graph.NodeText(n, source)
				scope := findEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeReferences})
			}
		}
	})

	return pr
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func isStdlib(importPath string) bool {
	return !strings.Contains(importPath, ".")
}

func extractReceiver(n *sitter.Node, source []byte) string {
	params := n.ChildByFieldName("receiver")
	if params == nil {
		return ""
	}
	var typeName string
	graph.WalkTree(params, func(c *sitter.Node) {
		if c.Type() == "type_identifier" && typeName == "" {
			typeName = graph.NodeText(c, source)
		}
	})
	return typeName
}

func isPackageScope(n *sitter.Node) bool {
	p := n.Parent()
	return p == nil || p.Type() == "source_file"
}

func goCallName(n *sitter.Node, source []byte) string {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		return graph.NodeText(fn, source)
	case "selector_expression":
		field := fn.ChildByFieldName("field")
		if field != nil {
			return graph.NodeText(field, source)
		}
	}
	return ""
}

func isGoReference(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "function_declaration", "method_declaration", "parameter_declaration",
		"var_spec", "const_spec", "type_spec", "import_spec",
		"short_var_declaration", "field_declaration", "package_clause":
		return false
	}
	return true
}

// findEnclosingFunc walks up the AST to find the enclosing function or method.
func findEnclosingFunc(n *sitter.Node, source []byte, filePath string) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "function_declaration":
			nameNode := p.ChildByFieldName("name")
			if nameNode != nil {
				return graph.FuncNodeID(filePath, graph.NodeText(nameNode, source))
			}
		case "method_declaration":
			nameNode := p.ChildByFieldName("name")
			if nameNode != nil {
				receiver := extractReceiver(p, source)
				return graph.MethodNodeID(filePath, receiver, graph.NodeText(nameNode, source))
			}
		}
	}
	return graph.FileNodeID(filePath)
}
