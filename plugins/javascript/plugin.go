package javascript

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"

	"github.com/chokevin/repograph/pkg/graph"
)

func init() {
	graph.RegisterPlugin(&Plugin{})
}

// Plugin implements graph.LanguagePlugin for JavaScript / TypeScript.
type Plugin struct{}

func (p *Plugin) Name() string               { return "javascript" }
func (p *Plugin) Extensions() []string        { return []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} }
func (p *Plugin) Language() *sitter.Language   { return javascript.GetLanguage() }

func (p *Plugin) Parse(filePath string, source []byte, root *sitter.Node) *graph.ParseResult {
	pr := &graph.ParseResult{}
	fileID := graph.FileNodeID(filePath)

	graph.WalkTree(root, func(n *sitter.Node) {
		switch n.Type() {

		// ── Imports ──────────────────────────────────────────────────────
		case "import_statement":
			src := graph.ChildByType(n, "string")
			if src != nil {
				target := resolveImport(filePath, graph.Unquote(graph.NodeText(src, source)))
				if target != "" {
					targetID := graph.FileNodeID(target)
					pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: targetID, Type: graph.EdgeImports})
				}
			}

		// require() calls handled via call_expression below

		// ── Function declarations ────────────────────────────────────────
		case "function_declaration":
			nameNode := graph.ChildByType(n, "identifier")
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
				Language: "javascript",
				Exported: graph.IsExported(name, "javascript"),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})

		// ── Variable declarations (const/let/var) ────────────────────────
		case "lexical_declaration", "variable_declaration":
			if !isModuleScope(n) {
				return
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				decl := n.NamedChild(i)
				if decl.Type() != "variable_declarator" {
					continue
				}

				// Check for require() first — works with both `const x = require(...)` and `const { a, b } = require(...)`
				callExpr := graph.ChildByType(decl, "call_expression")
				if callExpr != nil {
					fnNode := graph.ChildByType(callExpr, "identifier")
					if fnNode != nil && graph.NodeText(fnNode, source) == "require" {
						args := graph.ChildByType(callExpr, "arguments")
						if args != nil {
							strNode := graph.ChildByType(args, "string")
							if strNode != nil {
								target := resolveImport(filePath, graph.Unquote(graph.NodeText(strNode, source)))
								if target != "" {
									pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: graph.FileNodeID(target), Type: graph.EdgeImports})
								}
							}
						}
						continue
					}
				}

				nameNode := graph.ChildByType(decl, "identifier")
				if nameNode == nil {
					continue
				}
				name := graph.NodeText(nameNode, source)

				// Arrow / function expression ⇒ treat as function
				value := graph.ChildByType(decl, "arrow_function")
				if value == nil {
					value = graph.ChildByType(decl, "function")
				}
				if value != nil {
					id := graph.FuncNodeID(filePath, name)
					pr.Nodes = append(pr.Nodes, &graph.Node{
						ID:       id,
						Type:     graph.NodeFunction,
						Name:     name,
						FilePath: filePath,
						Language: "javascript",
						Exported: graph.IsExported(name, "javascript"),
						Line:     int(n.StartPoint().Row) + 1,
						EndLine:  int(n.EndPoint().Row) + 1,
					})
					pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})
					continue
				}

				// Plain variable
				id := graph.VarNodeID(filePath, "module", name)
				pr.Nodes = append(pr.Nodes, &graph.Node{
					ID:       id,
					Type:     graph.NodeVariable,
					Name:     name,
					FilePath: filePath,
					Language: "javascript",
					Exported: graph.IsExported(name, "javascript"),
					Line:     int(n.StartPoint().Row) + 1,
				})
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: id, Type: graph.EdgeDefines})
			}

		// ── Classes ──────────────────────────────────────────────────────
		case "class_declaration":
			nameNode := graph.ChildByType(n, "identifier")
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
				Language: "javascript",
				Exported: graph.IsExported(name, "javascript"),
				Line:     int(n.StartPoint().Row) + 1,
				EndLine:  int(n.EndPoint().Row) + 1,
			})
			pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: classID, Type: graph.EdgeDefines})

			// Inheritance
			heritage := graph.ChildByType(n, "class_heritage")
			if heritage != nil {
				baseNode := graph.ChildByType(heritage, "identifier")
				if baseNode != nil {
					baseName := graph.NodeText(baseNode, source)
					pr.Edges = append(pr.Edges, &graph.Edge{
						FromID: classID,
						ToID:   "unresolved:" + baseName,
						Type:   graph.EdgeInherits,
					})
				}
			}

			// Methods
			body := graph.ChildByType(n, "class_body")
			if body != nil {
				for _, m := range graph.ChildrenByType(body, "method_definition") {
					mName := methodName(m, source)
					if mName == "" {
						continue
					}
					mID := graph.MethodNodeID(filePath, name, mName)
					pr.Nodes = append(pr.Nodes, &graph.Node{
						ID:       mID,
						Type:     graph.NodeMethod,
						Name:     mName,
						FilePath: filePath,
						Language: "javascript",
						Parent:   classID,
						Line:     int(m.StartPoint().Row) + 1,
						EndLine:  int(m.EndPoint().Row) + 1,
					})
					pr.Edges = append(pr.Edges, &graph.Edge{FromID: mID, ToID: classID, Type: graph.EdgeMethodOf})
					pr.Edges = append(pr.Edges, &graph.Edge{FromID: fileID, ToID: mID, Type: graph.EdgeDefines})
				}
			}

		// ── Call expressions ─────────────────────────────────────────────
		case "call_expression":
			name := callName(n, source)
			if name != "" && name != "require" {
				scope := jsFindEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeCalls})
			}

		// ── Identifier references ────────────────────────────────────────
		case "identifier":
			if isReference(n) {
				name := graph.NodeText(n, source)
				scope := jsFindEnclosingFunc(n, source, filePath)
				pr.Edges = append(pr.Edges, &graph.Edge{FromID: scope, ToID: "unresolved:" + name, Type: graph.EdgeReferences})
			}
		}
	})

	return pr
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func resolveImport(fromFile, spec string) string {
	if !strings.HasPrefix(spec, ".") {
		return "" // external package
	}
	dir := filepath.Dir(fromFile)
	resolved := filepath.Join(dir, spec)
	resolved = filepath.Clean(resolved)
	// Normalise potential missing extension.
	if filepath.Ext(resolved) == "" {
		resolved += ".js"
	}
	return resolved
}

func isModuleScope(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return true
	}
	t := p.Type()
	return t == "program" || t == "export_statement"
}

func methodName(m *sitter.Node, source []byte) string {
	nameNode := graph.ChildByType(m, "property_identifier")
	if nameNode != nil {
		return graph.NodeText(nameNode, source)
	}
	return ""
}

func callName(n *sitter.Node, source []byte) string {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		return graph.NodeText(fn, source)
	case "member_expression":
		prop := graph.ChildByType(fn, "property_identifier")
		if prop != nil {
			return graph.NodeText(prop, source)
		}
	}
	return ""
}

func isReference(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "variable_declarator", "function_declaration", "formal_parameters",
		"import_specifier", "import_clause", "class_declaration",
		"method_definition", "property_identifier", "shorthand_property_identifier_pattern":
		return false
	}
	return true
}

// jsFindEnclosingFunc walks up the AST to find the enclosing function, method, or arrow function.
func jsFindEnclosingFunc(n *sitter.Node, source []byte, filePath string) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "function_declaration":
			nameNode := graph.ChildByType(p, "identifier")
			if nameNode != nil {
				return graph.FuncNodeID(filePath, graph.NodeText(nameNode, source))
			}
		case "method_definition":
			mName := methodName(p, source)
			if mName != "" {
				// Find enclosing class.
				for cp := p.Parent(); cp != nil; cp = cp.Parent() {
					if cp.Type() == "class_declaration" {
						classNameNode := graph.ChildByType(cp, "identifier")
						if classNameNode != nil {
							return graph.MethodNodeID(filePath, graph.NodeText(classNameNode, source), mName)
						}
					}
				}
			}
		case "arrow_function", "function":
			// Named arrow/function expressions: const foo = () => {}
			if pp := p.Parent(); pp != nil && pp.Type() == "variable_declarator" {
				nameNode := graph.ChildByType(pp, "identifier")
				if nameNode != nil {
					return graph.FuncNodeID(filePath, graph.NodeText(nameNode, source))
				}
			}
		}
	}
	return graph.FileNodeID(filePath)
}
