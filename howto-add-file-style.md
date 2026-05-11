# How to Add File-Style MCP Tools

This guide explains how to build an MCP server that serves content from a local directory tree. The pattern is exemplified by `xmlui-mcp`, which exposes XMLUI's documentation and source files to an AI assistant through several tool types: **list**, **named-item lookup**, **read-by-path**, **search**, **section-scoped search/list**, **external-content search**, and **session management**.

When you follow this guide you will end up with a project structured like `xmlui-mcp`, not an extension of the `general-mcp` skeleton. Replace the `xmlui_` prefix throughout with your own domain prefix.

---

## What "file style" means

The server holds a `rootDir` pointing at a directory on disk — either a local path supplied at startup, or a versioned archive downloaded and cached automatically. Tools let the AI navigate and read files from that tree without writing anything. Path safety and extension whitelists prevent the AI from escaping the tree or reading sensitive files.

Reference implementation: `../xmlui-mcp`.

---

## Overview of changes

| What | Where |
|------|-------|
| Add `-root` (or content-specific) CLI flag | `cmd/<yourproject>-mcp/main.go` |
| Add content root to `ServerConfig` | `pkg/<yourproject>mcp/server.go` |
| Add path resolver | new `server/paths.go` |
| Add list tool | new `server/list_<content>.go` |
| Add named-item lookup tool | new `server/<content>_docs.go` |
| Add read-file tool | new `server/read_file.go` |
| Add search tool | new `server/search.go` |
| Add section-scoped search/list tools | new `server/<section>.go` |
| Add external content search tool (optional) | new `server/examples.go` |
| Add session management tools | `pkg/<yourproject>mcp/server.go` (inline) |
| Auto-inject rules into default session at startup | `pkg/<yourproject>mcp/server.go` |
| Wire all tools in `setupTools()` | `pkg/<yourproject>mcp/server.go` |
| Update rules prompt with real tool names | `pkg/<yourproject>mcp/server.go` |

---

## Step 1 — Add a content-root flag

In `cmd/<yourproject>-mcp/main.go`, expose a flag for the content root and pass it through `ServerConfig`. `xmlui-mcp` downloads its content automatically and passes the cache path, but a local path flag is the simplest starting point:

```go
rootDir := flag.String("root", "", "Root directory of the content tree (required)")
flag.Parse()

if *rootDir == "" {
    fmt.Fprintln(os.Stderr, "Error: -root is required")
    os.Exit(1)
}

config := mymcp.ServerConfig{
    RootDir:  *rootDir,
    HTTPMode: *httpMode,
    Port:     *port,
}
```

Add `RootDir string` to `ServerConfig` in `pkg/<yourproject>mcp/server.go`.

See `../xmlui-mcp/cmd/xmlui-mcp/main.go` for the version that uses `-xmlui-version` and `-example` flags instead, with the root resolved at runtime by the downloader.

---

## Step 2 — Add a path resolver

Create `server/paths.go`. Its job is to map named logical sections (docs, source, howto, etc.) to subdirectory paths relative to `rootDir`. Loading from an optional `mcp-paths.json` manifest lets the content tree evolve independently of the binary.

```go
package server

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"
)

// ContentPaths holds named subdirectory paths relative to rootDir.
// Loaded from mcp-paths.json if present; otherwise uses compiled defaults.
type ContentPaths struct {
    Docs   string `json:"docs"`
    Source string `json:"source"`
    Howto  string `json:"howto"`
}

var (
    globalPaths     *ContentPaths
    globalPathsOnce sync.Once
)

func GetContentPaths(rootDir string) *ContentPaths {
    globalPathsOnce.Do(func() { globalPaths = loadContentPaths(rootDir) })
    return globalPaths
}

func loadContentPaths(rootDir string) *ContentPaths {
    data, err := os.ReadFile(filepath.Join(rootDir, "mcp-paths.json"))
    if err == nil {
        var p ContentPaths
        if json.Unmarshal(data, &p) == nil {
            return &p
        }
    }
    return &ContentPaths{Docs: "docs", Source: "src", Howto: "docs/howto"}
}
```

Add fields to match your content structure. `xmlui-mcp` has seven named sections (`ComponentDocs`, `ComponentSource`, `ExtensionDocs`, `ExtensionSource`, `Pages`, `Howto`, `Blog`) and validates each resolved path exists on startup, printing a warning per missing path. See `../xmlui-mcp/server/paths.go`.

---

## Step 3 — Add a list tool

Walk the content tree and return a structured listing. Name the tool after what it lists — `xmlui-mcp` calls it `xmlui_list_components` because it lists components.

```go
package server

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"

    "github.com/mark3labs/mcp-go/mcp"
)

func NewListContentTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_list_content",
        mcp.WithDescription("Lists all available content files."),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        paths := GetContentPaths(rootDir)
        docsRoot := filepath.Join(rootDir, paths.Docs)

        var files []string
        _ = filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
            if err != nil || d.IsDir() {
                return err
            }
            if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdx") {
                rel, _ := filepath.Rel(rootDir, path)
                files = append(files, rel)
            }
            return nil
        })

        sort.Strings(files)
        return mcp.NewToolResultText(strings.Join(files, "\n")), nil
    }

    return tool, handler
}
```

`xmlui-mcp` groups results by top-level subdirectory and collapses `App/App.md` → `App`, making the listing readable as a component index. It also appends the tool call the AI should make next per entry (`call xmlui_component_docs with component: "App"`). See `../xmlui-mcp/server/list_components.go`.

---

## Step 4 — Add a read-file tool

Two safety checks are mandatory and must appear in every read-file tool:

1. **Extension whitelist** — reject anything not in your allowed set.
2. **Prefix check** — after `filepath.Join`, verify the resolved absolute path still begins with `rootDir`. This blocks `../` traversal.

```go
package server

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/mark3labs/mcp-go/mcp"
)

var allowedExts = map[string]bool{".md": true, ".mdx": true}

func NewReadFileTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_read_file",
        mcp.WithDescription("Reads a file from the content tree by relative path."),
        mcp.WithString("path", mcp.Required(), mcp.Description("Relative path, e.g. docs/intro.md")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        rel, _ := req.Params.Arguments["path"].(string)
        if rel == "" {
            return mcp.NewToolResultError("'path' is required"), nil
        }

        if !allowedExts[strings.ToLower(filepath.Ext(rel))] {
            return mcp.NewToolResultError(fmt.Sprintf("extension %q not allowed", filepath.Ext(rel))), nil
        }

        full := filepath.Join(rootDir, rel)
        if !strings.HasPrefix(full, filepath.Clean(rootDir)+string(filepath.Separator)) {
            return mcp.NewToolResultError("path escapes root directory"), nil
        }

        content, err := os.ReadFile(full)
        if err != nil {
            return mcp.NewToolResultError(fmt.Sprintf("read failed: %v", err)), nil
        }
        return mcp.NewToolResultText(string(content)), nil
    }

    return tool, handler
}
```

`xmlui-mcp` allows `.mdx`, `.tsx`, `.scss`, and `.md`, and validates against each named section individually rather than just the root, so the AI can only read files that fall within a recognised section. See `../xmlui-mcp/server/read_file.go`.

---

## Step 4a — Add a named-item lookup tool

The read-file tool takes a *path*. A named-item lookup tool takes a *name* and returns the corresponding doc. This is the pattern behind `xmlui_component_docs`: the AI calls it with `component: "Button"` and gets the Button documentation without knowing its path on disk.

```go
func NewItemDocsTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_item_docs",
        mcp.WithDescription("Returns documentation for the named item."),
        mcp.WithString("item", mcp.Required(), mcp.Description("Item name, e.g. 'Button'")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        name, _ := req.Params.Arguments["item"].(string)
        if name == "" {
            return mcp.NewToolResultError("'item' is required"), nil
        }
        paths := GetContentPaths(rootDir)
        content, err := os.ReadFile(filepath.Join(rootDir, paths.Docs, name+".md"))
        if err != nil {
            return mcp.NewToolResultError(fmt.Sprintf("item %q not found", name)), nil
        }
        return mcp.NewToolResultText(string(content)), nil
    }

    return tool, handler
}
```

`xmlui-mcp`'s version (`server/component_docs.go`) adds three capabilities beyond this skeleton:

1. **Thin-doc supplementation** — if the file is under 500 characters, it fetches supplemental content from a parent component (e.g. `CVStack → Stack`) and extracts `Properties` / `Events` sections via `extractSections`. This prevents the AI from seeing empty docs.
2. **Parent component map** — a `map[string]string` maps variant names to their canonical parent (`DropdownButton → Button`, `VStack → Stack`, etc.). Consulted before falling back to source.
3. **Source URL suffix** — appends `ComponentURL(name)` to every response so the AI can cite its source.

See `../xmlui-mcp/server/component_docs.go` for the full implementation.

---

## Step 5 — Add a search tool

### Basic version

A line-level case-insensitive scan gets you started:

```go
package server

import (
    "bufio"
    "context"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/mark3labs/mcp-go/mcp"
)

func NewSearchTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_search",
        mcp.WithDescription("Searches content files for a query. Returns matching lines with file paths."),
        mcp.WithString("query", mcp.Required(), mcp.Description("Search term")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        query := strings.TrimSpace(req.Params.Arguments["query"].(string))
        if query == "" {
            return mcp.NewToolResultError("'query' is required"), nil
        }
        lower := strings.ToLower(query)

        paths := GetContentPaths(rootDir)
        var results []string
        _ = filepath.WalkDir(filepath.Join(rootDir, paths.Docs), func(path string, d os.DirEntry, err error) error {
            if err != nil || d.IsDir() {
                return err
            }
            f, _ := os.Open(path)
            if f == nil {
                return nil
            }
            defer f.Close()
            rel, _ := filepath.Rel(rootDir, path)
            scanner := bufio.NewScanner(f)
            lineNum := 0
            for scanner.Scan() {
                lineNum++
                line := scanner.Text()
                if strings.Contains(strings.ToLower(line), lower) {
                    results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNum, strings.TrimSpace(line)))
                }
            }
            return nil
        })

        if len(results) == 0 {
            return mcp.NewToolResultText("No matches found."), nil
        }
        if len(results) > 50 {
            results = results[:50]
        }
        return mcp.NewToolResultText(strings.Join(results, "\n")), nil
    }

    return tool, handler
}
```

### Advanced version — the search mediator

`xmlui-mcp` replaces the grep above with a staged mediator that runs three passes:

1. **Exact** — full query as written
2. **Relaxed** — stopwords removed
3. **Partial** — any word in the query matches (percentage-based threshold)

Results are ranked by a scoring function that weights term coverage, section (docs/howto score higher than source/blog), filename bonus, topic-index bonus from heading keywords, match density, and a deprecation penalty. The mediator appends agent guidance — confidence level, documentation URLs, and suggested next steps — as a plain-text block separated by `---` at the end of its output. A `MediatorJSON` struct is also returned by `ExecuteMediatedSearch` as a second return value (discarded in `search.go`), but no JSON is written into the human-readable output.

To use it, copy these files from `../xmlui-mcp/server/` into your `server/` package:

- `mediator.go` — core search engine and scoring
- `suggestions.go` — `suggestAlternatives()` and `levenshtein()`, called by `mediator.go` for "Did you mean?" results; **required** — `mediator.go` will not compile without it
- `topic_index.go` — heading-keyword index for bonus scoring
- `path_utils.go` — `commonParent()` utility
- `url_registry.go` — file path → documentation URL mapping

Then replace the handler body with a call to `ExecuteMediatedSearch`, passing a `MediatorConfig` that points `Roots` at your named sections. See `../xmlui-mcp/server/search.go` for the exact wiring.

---

## Step 5a — Add section-scoped search and list tools

When your content has distinct sections, create a dedicated search tool (and optionally a list tool) per section rather than making the AI pass a filter. Each is a thin wrapper around the mediator with `Roots`, `SectionKeys`, and `Classifier` scoped to one directory. `xmlui-mcp` has `xmlui_list_howto` and `xmlui_search_howto` in `server/howto.go`.

**List variant** — walks the section directory and returns one title per file:

```go
func NewListSectionTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_list_<section>",
        mcp.WithDescription("Lists all items in the <section> directory."),
    )
    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        paths := GetContentPaths(rootDir)
        sectionDir := filepath.Join(rootDir, paths.Howto) // replace with your section path
        var titles []string
        _ = filepath.WalkDir(sectionDir, func(path string, d os.DirEntry, err error) error {
            if err != nil || d.IsDir() {
                return nil
            }
            if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdx") {
                titles = append(titles, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
            }
            return nil
        })
        return mcp.NewToolResultText(strings.Join(titles, "\n")), nil
    }
    return tool, handler
}
```

`xmlui-mcp`'s list-howto tool parses `# ` headings from each file to return article titles rather than filenames. See `../xmlui-mcp/server/howto.go`.

**Search variant** — the classifier lambda is the key: since every file in this root belongs to the same section, it returns a constant:

```go
func NewSearchSectionTool(rootDir string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_search_<section>",
        mcp.WithDescription("Searches <section> content using the staged search mediator."),
        mcp.WithString("query", mcp.Required(), mcp.Description("Search term")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        query, _ := req.Params.Arguments["query"].(string)
        if strings.TrimSpace(query) == "" {
            return mcp.NewToolResultError("'query' is required"), nil
        }
        paths := GetContentPaths(rootDir)
        cfg := MediatorConfig{
            Roots:                 []string{filepath.Join(rootDir, paths.Howto)}, // replace with your section
            SectionKeys:           []string{"howtos"},
            PreferSections:        []string{"howtos"},
            MaxResults:            50,
            FileExtensions:        []string{".md", ".mdx"},
            Stopwords:             DefaultStopwords(),
            Synonyms:              DefaultSynonyms(),
            Classifier:            func(rel, absPath string) string { return "howtos" },
            EnableFilenameMatches: true,
        }
        human, _, err := ExecuteMediatedSearch(rootDir, cfg, query)
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        return mcp.NewToolResultText(human), nil
    }
    return tool, handler
}
```

Register both with `WithAnalytics` / `WithSearchAnalytics` respectively, same as the main tools.

---

## Step 5b — Add an external content search tool

`xmlui_examples` searches directories that live *outside* the main content root — local sample apps passed in via `-example` CLI flags. The tool takes `exampleRoots []string`, collected in `setupTools()` from `s.config.ExampleDirs`, and falls back gracefully when none are configured:

```go
func NewExamplesTool(exampleRoots []string) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("<yourprefix>_examples",
        mcp.WithDescription("Searches local sample apps for usage examples."),
        mcp.WithString("query", mcp.Required(), mcp.Description("Search term")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        if len(exampleRoots) == 0 {
            return mcp.NewToolResultText("No example directories configured."), nil
        }
        query, _ := req.Params.Arguments["query"].(string)
        if strings.TrimSpace(query) == "" {
            return mcp.NewToolResultError("'query' is required"), nil
        }
        cfg := MediatorConfig{
            Roots:                 exampleRoots,
            SectionKeys:           []string{"examples"},
            PreferSections:        []string{"examples"},
            MaxResults:            50,
            FileExtensions:        []string{".tsx", ".xmlui", ".mdx", ".md"},
            Stopwords:             DefaultStopwords(),
            Synonyms:              DefaultSynonyms(),
            Classifier:            func(rel, absPath string) string { return "examples" },
            EnableFilenameMatches: true,
        }
        homeDir := commonParent(exampleRoots) // from path_utils.go
        human, _, err := ExecuteMediatedSearch(homeDir, cfg, query)
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        return mcp.NewToolResultText(human), nil
    }
    return tool, handler
}
```

In `main.go`, expose a repeatable `-example` / `-e` flag and accumulate paths into `ServerConfig.ExampleDirs []string`. In `setupTools()`, build `exampleRoots` from `s.config.ExampleDirs` before passing to `NewExamplesTool`. See `../xmlui-mcp/server/examples.go` and `../xmlui-mcp/cmd/xmlui-mcp/main.go`.

---

## Step 6 — Wire the tools

In `setupTools()`, register every tool you created. Build example roots from `s.config.ExampleDirs` first, then register in order:

```go
func (s *MCPServer) setupTools() error {
    root := s.config.RootDir

    // Collect optional external example dirs
    var exampleRoots []string
    for _, d := range s.config.ExampleDirs {
        if d = strings.TrimSpace(d); d != "" {
            exampleRoots = append(exampleRoots, d)
        }
    }

    // Domain tools
    listTool, listHandler := mcpserver.NewListContentTool(root)
    s.mcpServer.AddTool(listTool, mcpserver.WithAnalytics("<yourprefix>_list_content", listHandler))
    s.tools = append(s.tools, listTool)

    itemDocsTool, itemDocsHandler := mcpserver.NewItemDocsTool(root)
    s.mcpServer.AddTool(itemDocsTool, mcpserver.WithAnalytics("<yourprefix>_item_docs", itemDocsHandler))
    s.tools = append(s.tools, itemDocsTool)

    readTool, readHandler := mcpserver.NewReadFileTool(root)
    s.mcpServer.AddTool(readTool, mcpserver.WithAnalytics("<yourprefix>_read_file", readHandler))
    s.tools = append(s.tools, readTool)

    searchTool, searchHandler := mcpserver.NewSearchTool(root, exampleRoots)
    s.mcpServer.AddTool(searchTool, mcpserver.WithSearchAnalytics("<yourprefix>_search", searchHandler))
    s.tools = append(s.tools, searchTool)

    listSectionTool, listSectionHandler := mcpserver.NewListSectionTool(root)
    s.mcpServer.AddTool(listSectionTool, mcpserver.WithAnalytics("<yourprefix>_list_<section>", listSectionHandler))
    s.tools = append(s.tools, listSectionTool)

    searchSectionTool, searchSectionHandler := mcpserver.NewSearchSectionTool(root)
    s.mcpServer.AddTool(searchSectionTool, mcpserver.WithSearchAnalytics("<yourprefix>_search_<section>", searchSectionHandler))
    s.tools = append(s.tools, searchSectionTool)

    examplesTool, examplesHandler := mcpserver.NewExamplesTool(exampleRoots)
    s.mcpServer.AddTool(examplesTool, mcpserver.WithSearchAnalytics("<yourprefix>_examples", examplesHandler))
    s.tools = append(s.tools, examplesTool)

    // Session management tools (inline — they reference s.sessionManager and s.promptHandlers)
    // See Step 6a below.

    return nil
}
```

Use `WithSearchAnalytics` for every search-style tool so query terms are captured as a separate record type in the analytics log.

---

## Step 6a — Add session management tools

Four tools are registered inline in `setupTools()` because they close over `s.sessionManager` and `s.promptHandlers`. They use `tool.InputSchema = mcp.ToolInputSchema{...}` directly (not `mcp.WithString`) so they can declare `default` values and mark parameters optional.

```go
// in setupTools(), after domain tools:

injectTool := mcp.NewTool("<yourprefix>_inject_prompt",
    mcp.WithDescription("Injects a prompt into the current session context"),
)
injectTool.InputSchema = mcp.ToolInputSchema{
    Type: "object",
    Properties: map[string]interface{}{
        "prompt_name": map[string]interface{}{
            "type":        "string",
            "description": "Name of the prompt to inject (e.g. '<yourprefix>_rules')",
            "default":     "<yourprefix>_rules",
        },
        "session_id": map[string]interface{}{
            "type":    "string",
            "description": "Session ID (defaults to 'default')",
            "default": "default",
        },
    },
    Required: []string{"prompt_name"},
}
s.mcpServer.AddTool(injectTool, mcpserver.WithAnalytics("<yourprefix>_inject_prompt",
    func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        promptName, _ := req.Params.Arguments["prompt_name"].(string)
        sessionID := "default"
        if id, ok := req.Params.Arguments["session_id"].(string); ok && id != "" {
            sessionID = id
        }
        resp, err := s.sessionManager.InjectPrompt(sessionID, promptName, s.promptHandlers)
        if err != nil || !resp.Success {
            return mcp.NewToolResultError("Failed to inject prompt"), nil
        }
        return mcp.NewToolResultText(fmt.Sprintf("Injected '%s' into session '%s'.", promptName, sessionID)), nil
    },
))
s.tools = append(s.tools, injectTool)

listPromptsTool := mcp.NewTool("<yourprefix>_list_prompts",
    mcp.WithDescription("Lists all available prompts that can be injected into session context"),
)
s.mcpServer.AddTool(listPromptsTool, mcpserver.WithAnalytics("<yourprefix>_list_prompts",
    func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        var out strings.Builder
        for _, p := range s.prompts {
            out.WriteString(fmt.Sprintf("- **%s**: %s\n", p.Name, p.Description))
        }
        out.WriteString("\nUse <yourprefix>_get_prompt to view content or <yourprefix>_inject_prompt to inject.")
        return mcp.NewToolResultText(out.String()), nil
    },
))
s.tools = append(s.tools, listPromptsTool)
```

The remaining two tools (`<yourprefix>_get_prompt` and `<yourprefix>_get_session_context`) follow the same inline closure pattern. See `../xmlui-mcp/pkg/xmluimcp/server.go` for their full implementations. `get_prompt` calls the stored `PromptHandler` to render the prompt content; `get_session_context` reads `s.sessionManager.GetOrCreateSession(sessionID)` and formats `InjectedPrompts` and `Context`.

---

## Step 7 — Update the rules prompt

Now that your tools are real, replace the generic placeholder text in `setupPrompts()` with actual tool names and workflow guidance tailored to your content. The `xmlui-mcp` rules prompt (`xmlui_rules`) illustrates the pattern: it names every tool, gives an ordered workflow (search → docs → source → trace), and includes domain-specific rules like "validate components against docs before writing code."

Write your equivalent rules to match your content domain.

---

## Step 7a — Auto-inject rules at startup

Without auto-injection, the rules prompt exists but only activates if the AI explicitly calls `<yourprefix>_inject_prompt` or the client sends a `GetPrompt` request. In `NewServer()`, after `setupPrompts()`, inject the rules into the default session so they are active from the first message:

```go
// in NewServer(), after setupPrompts():

_, err = sessionManager.InjectPrompt("default", "<yourprefix>_rules", yourServer.promptHandlers)
if err != nil {
    mcpserver.WriteDebugLog("Failed to auto-inject rules: %v\n", err)
}
```

If you also have a conditional update notice (see Optional additions), inject it **before** the rules so the rules land last and stay at the top of the AI's context:

```go
if _, exists := yourServer.promptHandlers["<yourprefix>_update_notice"]; exists {
    sessionManager.InjectPrompt("default", "<yourprefix>_update_notice", yourServer.promptHandlers)
}
sessionManager.InjectPrompt("default", "<yourprefix>_rules", yourServer.promptHandlers)
```

See `../xmlui-mcp/pkg/xmluimcp/server.go` (`NewServer`) for the exact pattern.

---

## Optional additions

### Named subdirectories with mcp-paths.json

When the tree has multiple distinct sections, add corresponding fields to `ContentPaths` and ship an `mcp-paths.json` in the root of your content. Tools resolve paths like `filepath.Join(rootDir, paths.Howto)` rather than hard-coding directory names. This lets the content tree restructure without recompiling. See `../xmlui-mcp/server/paths.go` for the full pattern with per-path existence validation on startup.

### URL registry

If your files correspond to pages on a public site, add a URL registry that maps relative file paths to their canonical URLs. The AI can then include live links in its responses. See `../xmlui-mcp/server/url_registry.go`.

### Version update check and conditional notice prompt

If the server is distributed as a versioned CLI binary, surface a notice when a newer release exists. This requires three pieces:

**1. `CLIVersion` in `ServerConfig`** — set at build time via ldflags:

```
go build -ldflags "-X yourpkg.CLIVersion=1.2.3" ...
```

Store it as `CLIVersion string` in `ServerConfig` and pass through to `NewServer`.

**2. `checkForUpdate(current string) string`** — fetch the latest GitHub release tag and compare semver. Returns a notice string if an update is available, empty string otherwise:

```go
func checkForUpdate(current string) string {
    if current == "" || current == "dev" { return "" }
    // GET https://api.github.com/repos/<org>/<repo>/releases/latest
    // decode tag_name, compare with semverLessThan(current, latest)
    // return notice string or ""
}
```

See `../xmlui-mcp/pkg/xmluimcp/update_check.go` for the full `semverLessThan` + `normalizeSemver` implementation (strips `v` prefix, pre-release suffixes, and build metadata before comparing numerically).

**3. Register and auto-inject the notice prompt** — in `setupPrompts()`:

```go
func (s *MCPServer) setupPrompts() error {
    notice := checkForUpdate(s.config.CLIVersion)

    // ... register rules prompt ...

    if notice != "" {
        updateHandler := func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
            return mcp.NewGetPromptResult("Update notice", []mcp.PromptMessage{
                mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(notice)),
            }), nil
        }
        updatePrompt := mcp.NewPrompt("<yourprefix>_update_notice",
            mcp.WithPromptDescription("Notify that a newer version is available"))
        s.prompts = append(s.prompts, updatePrompt)
        s.promptHandlers["<yourprefix>_update_notice"] = updateHandler
        s.mcpServer.AddPrompt(updatePrompt, updateHandler)
    }
    return nil
}
```

Then inject it before the rules in `NewServer()` as shown in Step 7a.

---

### Remote content — downloading from GitHub releases

If your content is versioned and published as a GitHub release archive, the server can download and cache it at startup instead of requiring a local `-root` flag. The pattern:

- Accept a `-version` flag (or default to latest)
- On startup, check a platform-specific cache directory for that version
- If absent, query the GitHub releases API, download the zip, extract atomically
- Keep the N most recent versions; evict older ones

This requires file-based locking so concurrent server instances don't corrupt the cache. See `../xmlui-mcp/pkg/xmluimcp/repo_downloader.go` for the full implementation including the Windows vs. Unix locking split.
