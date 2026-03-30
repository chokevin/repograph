package python

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"

	"github.com/chokevin/repograph/pkg/graph"
)

func init() {
	graph.RegisterPlugin(&Plugin{})
}

// Plugin implements graph.LanguagePlugin for Python.
type Plugin struct{}

func (p *Plugin) Name() string             { return "python" }
func (p *Plugin) Extensions() []string     { return []string{".py"} }
func (p *Plugin) Language() *sitter.Language { return python.GetLanguage() }

func (p *Plugin) Parse(filePath string, source []byte, root *sitter.Node) *graph.ParseResult {
	pr := &graph.ParseResult{}
	fileID := graph.FileNodeID(filePath)

	graph.WalkTree(root, func(n *sitter.Node) {
		switch n.Type() {

		// ── import foo ──────────────────────────────────────────────────
		case "import_statement":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				child := n.NamedChild(i)
				if child.Type() == "dotted_name" {
					modName := graph.NodeText(child, source)
					pr.Edges = append(pr.Edges, &graph.Edge{
						FromID: fileID,
						ToID:   graph.FileNodeID(modName),
						Type:   graph.EdgeImports,
					})
				}
			}

		// ── from foo import bar ─────────────────────────────────────────
		case "import_from_statement":
			modNode := graph.ChildByType(n, "dotted_name")
			if modNode == nil {
				modNode = graph.ChildByType(n, "relative_import")
			}
			if modNode != nil {
				modName := graph.NodeText(modNode, source)
				pr.Edges = append(pr.Edges, &graph.Edge{
					FromID: fileID,
					ToID:   graph.FileNodeID(modName),
					Type:   graph.EdgeImports,
				})
			}

		// ── def foo(): ──────────────────────────────────────────────────
		case "function_definition":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return
			}
			name := graph.NodeText(nameNode, source)

			// Method if inside a class body.
			if className := enclosingClassName(n, source); className != "" {
				mID := graph.MethodNodeID(filePath, className, name)
				pr.Nodes = append(pr.Nodes, &graph.Node{
					ID:       mID,
					Type:     graph.NodeMethod,
					Name:     name,
					FilePath: filePath,
					Language: "python",
					Exported: graph.IsExported(name, "python"),
					Parent:   graph.ClassNodeID(filePath, className),
					Line:     int(n.StartPoint().Row) + 1,
					EndLine:  int(n.EndPoint().Row) + 1,
				})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: mID, ToID: graph.ClassNodeID(filePath, className), Type: graph.EdgeMethodOf})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: mID, Type: graph.EdgeDefines})
				return
			}

			id := graph.FuncNodeID(filePath, name)
			pr.Nodes = append(pr.Nodes, &graph.Node{
				ID:       id,
				Type:     graph.NodeFunction,
				Name:     name,
				FilePath: filePath,
				Language: "python",
				Exported: graph.IsExported(name, "python"),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})

		// ── class Foo: ──────────────────────────────────────────────────
		case "class_definition":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return
			}
			name := graph.NodeText(nameNode, source)
			classID := graph.ClassNodeID(filePath, name)
			pr.Nodes = append(pr.Nodes, &graph.Node{
				ID:       classID,
				Type:     graph.NodeClass,
				Name:     name,
				FilePath: filePath,
				Language: "python",
				Exported: graph.IsExported(name, "python"),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: classID, Type: graph.EdgeDefines})

			// Inheritance: class Foo(Bar, Baz)
			superclasses := n.ChildByFieldName("superclasses")
			if superclasses != nil {
				for i := 0; i < int(superclasses.NamedChildCount()); i++ {
					base := superclasses.NamedChild(i)
					if base.Type() == "identifier" {
						baseName := graph.NodeText(base, source)
						pr.Edges = append(pr.Edges, &graph.Edge{
							FromID: classID,
							ToID:   "unresolved:" + baseName,
							Type:   graph.EdgeInherits,
						})
					}
				}
			}

		// ── Module-level assignments: x = ... ───────────────────────────
		case "expression_statement":
			if !isModuleScope(n) {
				return
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				child := n.NamedChild(i)
				if child.Type() != "assignment" {
					continue
				}
				left := child.ChildByFieldName("left")
				if left == nil || left.Type() != "identifier" {
					continue
				}
				name := graph.NodeText(left, source)
				id := graph.VarNodeID(filePath, "module", name)
				pr.Nodes = append(pr.Nodes, &graph.Node{
					ID:       id,
					Type:     graph.NodeVariable,
					Name:     name,
					FilePath: filePath,
					Language: "python",
					Exported: graph.IsExported(name, "python"),
					Line:     int(child.StartPoint().Row) + 1,
				})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})
			}

		// ── Call expressions ─────────────────────────────────────────────
		case "call":
			name := pyCallName(n, source)
			if name != "" {
				scope := pyFindEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeCalls})
			}

		// ── Identifier references ────────────────────────────────────────
		case "identifier":
			if isPyReference(n) {
				name := graph.NodeText(n, source)
				scope := pyFindEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeReferences})
			}
		}
	})

	return pr
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func enclosingClassName(n *sitter.Node, source []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_definition" {
			nameNode := p.ChildByFieldName("name")
			if nameNode != nil {
				return graph.NodeText(nameNode, source)
			}
		}
	}
	return ""
}

func isModuleScope(n *sitter.Node) bool {
	p := n.Parent()
	return p == nil || p.Type() == "module"
}

func pyCallName(n *sitter.Node, source []byte) string {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		return graph.NodeText(fn, source)
	case "attribute":
		attr := fn.ChildByFieldName("attribute")
		if attr != nil {
			return graph.NodeText(attr, source)
		}
	}
	return ""
}

func isPyReference(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "function_definition", "class_definition", "parameters",
		"import_statement", "import_from_statement", "dotted_name",
		"aliased_import", "for_statement", "assignment":
		return false
	}
	return true
}

// pyFindEnclosingFunc walks up the AST to find the enclosing function or method.
func pyFindEnclosingFunc(n *sitter.Node, source []byte, filePath string) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "function_definition" {
			nameNode := p.ChildByFieldName("name")
			if nameNode != nil {
				name := graph.NodeText(nameNode, source)
				className := enclosingClassName(p, source)
				if className != "" {
					return graph.MethodNodeID(filePath, className, name)
				}
				return graph.FuncNodeID(filePath, name)
			}
		}
	}
	return graph.FileNodeID(filePath)
}
