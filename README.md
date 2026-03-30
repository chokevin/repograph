# repograph

A fast, pluggable code graph builder for repository-level context. Single Go binary, zero runtime dependencies.

Built for multi-agent coding tools that need to understand repo structure before writing code.

## Why?

LLM coding agents typically get a flat file list. They don't know which files import which, what functions call what, or how data flows through the codebase. This leads to:

- Agents editing the wrong files
- Missing cross-file dependencies
- Duplicate work across parallel agents
- Poor task decomposition (can't split by module boundaries)

**repograph** builds a dependency graph in <2 seconds and outputs LLM-ready context.

Inspired by [CGM (NeurIPS 2025)](https://arxiv.org/abs/2505.16901) — but designed to be pluggable, fast, and work with any LLM.

## Quick Start

```bash
# Install
go install github.com/chokevin/repograph/cmd/repograph@latest

# Build graph and show summary
repograph --action=summary --repo=./my-project

# Get related files for a specific file
repograph --action=related --file=src/auth/login.ts --depth=2

# Search by keywords
repograph --action=context --query="authentication login JWT"

# Get LLM-ready prompt context for a file
repograph --action=prompt --file=src/api/routes.go
```

## Output Examples

### Summary (for task decomposition prompts)
```
Repository Graph: 205 files, 758 functions, 190 classes, 42 methods, 312 variables, 13882 edges
Languages: go, javascript, python

─ src/auth/login.ts → imports: src/db/users.ts, src/utils/jwt.ts → exports: login, logout, refreshToken
─ src/api/routes.go → imports: src/auth, src/handlers → exports: NewRouter, RegisterRoutes
─ src/db/users.ts → imported by: src/auth/login.ts, src/api/admin.ts → exports: UserModel, findUser
```

### Per-file prompt (for agent task context)
```
── src/auth/login.ts
   imports: src/db/users.ts, src/utils/jwt.ts
   imported by: src/api/routes.ts, tests/auth.test.ts
   fn login (exported)
   fn logout (exported)
   fn refreshToken (exported)
   var TOKEN_EXPIRY
── src/db/users.ts
   imported by: src/auth/login.ts, src/api/admin.ts
   class UserModel (exported)
   fn findUser (exported)
```

## Architecture

```
┌─────────────────────────────────────────────┐
│                  CLI / API                   │
├─────────────────────────────────────────────┤
│              Query Engine                    │
│   QueryRelated │ QueryContext │ FormatPrompt │
├─────────────────────────────────────────────┤
│           Parser Orchestrator                │
│    Scanner → Workers → Edge Resolution       │
├──────────┬──────────┬──────────┬────────────┤
│ JS/TS    │ Go       │ Python   │ Your Lang  │
│ Plugin   │ Plugin   │ Plugin   │ Plugin     │
├──────────┴──────────┴──────────┴────────────┤
│            tree-sitter (CGo)                 │
└─────────────────────────────────────────────┘
```

### Graph Model (CGM-inspired)

**7 Node Types:**
| Type | Description | Example |
|------|-------------|---------|
| `repo` | Repository root | `repo:my-project` |
| `dir` | Directory | `dir:src/auth` |
| `file` | Source file | `file:src/auth/login.ts` |
| `class` | Class/struct/interface | `class:src/auth/login.ts:UserService` |
| `function` | Function/arrow fn | `func:src/auth/login.ts:login` |
| `method` | Class method | `method:src/auth/login.ts:UserService.authenticate` |
| `variable` | Module-scope variable | `var:src/auth/login.ts:module.TOKEN_EXPIRY` |

**7 Edge Types:**
| Type | Description | Example |
|------|-------------|---------|
| `contains` | Hierarchical (dir→file, file→func) | `dir:src` → `file:src/main.go` |
| `imports` | Module imports | `file:a.js` → `file:b.js` |
| `calls` | Function/method calls | `func:a.js:start` → `func:b.js:init` |
| `references` | Variable usage | `func:a.js:start` → `var:config.js:module.PORT` |
| `inherits` | Class inheritance | `class:dog.py:Dog` → `class:animal.py:Animal` |
| `defines` | Symbol definition in file | `file:a.js` → `func:a.js:start` |
| `method_of` | Method belongs to class | `method:a.js:Foo.bar` → `class:a.js:Foo` |

## Writing a Language Plugin

Plugins implement one interface:

```go
package mylang

import (
    "github.com/chokevin/repograph/pkg/graph"
    sitter "github.com/smacker/go-tree-sitter"
    // Import your tree-sitter grammar
    mylang "github.com/my/tree-sitter-mylang"
)

func init() {
    graph.RegisterPlugin(&Plugin{})
}

type Plugin struct{}

func (p *Plugin) Name() string                { return "mylang" }
func (p *Plugin) Extensions() []string        { return []string{".ml"} }
func (p *Plugin) Language() *sitter.Language   { return mylang.GetLanguage() }

func (p *Plugin) Parse(filePath string, source []byte, root *sitter.Node) *graph.ParseResult {
    result := &graph.ParseResult{}
    
    graph.WalkTree(root, func(node *sitter.Node) {
        switch node.Type() {
        case "function_definition":
            name := graph.NodeText(graph.ChildByType(node, "identifier"), source)
            result.Nodes = append(result.Nodes, &graph.Node{
                ID:       graph.FuncNodeID(filePath, name),
                Type:     graph.NodeFunction,
                Name:     name,
                FilePath: filePath,
                Line:     int(node.StartPoint().Row) + 1,
                Exported: graph.IsExported(name, "mylang"),
            })
        // ... handle imports, classes, calls, variables
        }
    })
    
    return result
}
```

Then import it in your binary:
```go
import _ "github.com/my/repograph-mylang"
```

## CGM Comparison

| Feature | CGM | repograph |
|---------|-----|-----------|
| Language | Python + PyTorch | Go (single binary) |
| Model dependency | Requires CGM-72B fine-tuned model | **Works with any LLM** |
| Graph builder | Coupled to pipeline | **Standalone CLI** |
| Pluggable languages | No | **Yes — plugin interface** |
| Build speed | Minutes (embedding generation) | **<2 seconds** |
| Variable tracking | ✅ | ✅ |
| Containment hierarchy | ✅ | ✅ |
| Node embeddings | ✅ (CodeT5+) | ❌ (planned) |
| R4 pipeline | ✅ | ❌ (planned) |

## CLI Reference

```
repograph [flags]

Flags:
  --repo PATH       Repository path (default: .)
  --action ACTION   build | summary | related | context | prompt | file
  --file PATH       File path (for related, prompt, file)
  --query TEXT      Search query (for context)
  --depth N         Hop depth for related (default: 2)
  --format FORMAT   text | json (default: text)
```

## Programmatic Usage

```go
import (
    "github.com/chokevin/repograph/pkg/graph"
    "github.com/chokevin/repograph/pkg/parser"
    "github.com/chokevin/repograph/pkg/query"
    
    // Register language plugins
    _ "github.com/chokevin/repograph/plugins/javascript"
    _ "github.com/chokevin/repograph/plugins/golang"
    _ "github.com/chokevin/repograph/plugins/python"
)

g, err := parser.BuildGraph("./my-repo", nil)

// Summary for decomposition prompt
summary := g.Summary()

// Related files for task context
result := query.QueryRelated(g, "src/auth/login.ts", 2)
prompt := query.FormatForPrompt(result)

// Keyword search
result := query.QueryContext(g, "authentication JWT token")
```

## License

MIT
