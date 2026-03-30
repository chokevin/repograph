package graph

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// ParseResult contains everything a language plugin extracts from a single file.
type ParseResult struct {
	Nodes []*Node
	Edges []*Edge
}

// LanguagePlugin is the interface that language-specific parsers must implement.
// Each plugin handles one or more languages and uses tree-sitter to extract
// nodes (functions, classes, variables, etc.) and edges (imports, calls, references).
type LanguagePlugin interface {
	// Name returns the plugin's display name (e.g., "javascript", "go", "python").
	Name() string

	// Extensions returns the file extensions this plugin handles (e.g., [".js", ".ts"]).
	Extensions() []string

	// Language returns the tree-sitter language grammar.
	Language() *sitter.Language

	// Parse extracts nodes and edges from a single file's AST.
	// filePath is the relative path within the repo.
	// source is the raw file contents.
	// root is the parsed tree-sitter root node.
	Parse(filePath string, source []byte, root *sitter.Node) *ParseResult
}

// pluginRegistry holds all registered language plugins keyed by extension.
var pluginRegistry = struct {
	mu      sync.RWMutex
	byExt   map[string]LanguagePlugin
	byName  map[string]LanguagePlugin
	plugins []LanguagePlugin
}{
	byExt:  make(map[string]LanguagePlugin),
	byName: make(map[string]LanguagePlugin),
}

// RegisterPlugin adds a language plugin to the global registry.
// Call this from plugin init() functions.
func RegisterPlugin(p LanguagePlugin) {
	pluginRegistry.mu.Lock()
	defer pluginRegistry.mu.Unlock()
	pluginRegistry.byName[p.Name()] = p
	pluginRegistry.plugins = append(pluginRegistry.plugins, p)
	for _, ext := range p.Extensions() {
		pluginRegistry.byExt[ext] = p
	}
}

// PluginForExtension returns the registered plugin for a file extension.
func PluginForExtension(ext string) LanguagePlugin {
	pluginRegistry.mu.RLock()
	defer pluginRegistry.mu.RUnlock()
	return pluginRegistry.byExt[ext]
}

// PluginForName returns the registered plugin by name.
func PluginForName(name string) LanguagePlugin {
	pluginRegistry.mu.RLock()
	defer pluginRegistry.mu.RUnlock()
	return pluginRegistry.byName[name]
}

// RegisteredPlugins returns all registered plugins.
func RegisteredPlugins() []LanguagePlugin {
	pluginRegistry.mu.RLock()
	defer pluginRegistry.mu.RUnlock()
	out := make([]LanguagePlugin, len(pluginRegistry.plugins))
	copy(out, pluginRegistry.plugins)
	return out
}

// LanguageForExtension returns the language name for a file extension, or "".
func LanguageForExtension(ext string) string {
	p := PluginForExtension(ext)
	if p == nil {
		return ""
	}
	return p.Name()
}
