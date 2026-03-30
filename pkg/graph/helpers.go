package graph

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// WalkTree calls fn for every node in the AST rooted at root (depth-first).
func WalkTree(root *sitter.Node, fn func(*sitter.Node)) {
	if root == nil {
		return
	}
	fn(root)
	for i := 0; i < int(root.ChildCount()); i++ {
		WalkTree(root.Child(i), fn)
	}
}

// NodeText extracts the source text for a tree-sitter node.
func NodeText(node *sitter.Node, source []byte) string {
	start := node.StartByte()
	end := node.EndByte()
	if int(end) > len(source) {
		end = uint32(len(source))
	}
	return string(source[start:end])
}

// Unquote removes surrounding quotes from a string literal.
func Unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// IsExported checks if an identifier is exported.
// Go: starts with uppercase. JS/Python: doesn't start with underscore.
func IsExported(name, language string) bool {
	if name == "" {
		return false
	}
	switch language {
	case "go":
		return name[0] >= 'A' && name[0] <= 'Z'
	case "python":
		return !strings.HasPrefix(name, "_")
	default:
		return true // JS/TS: assume exported unless prefixed
	}
}

// ChildByType returns the first named child with the given type, or nil.
func ChildByType(node *sitter.Node, typeName string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == typeName {
			return child
		}
	}
	return nil
}

// ChildrenByType returns all named children with the given type.
func ChildrenByType(node *sitter.Node, typeName string) []*sitter.Node {
	var result []*sitter.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == typeName {
			result = append(result, child)
		}
	}
	return result
}
