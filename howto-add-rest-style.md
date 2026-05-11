# How to add a REST-backed MCP tool

This guide describes the pattern used in `obsidian-mcp` for wiring a REST API endpoint into an MCP tool. Every tool in `server/` follows the same shape — learn it once and every addition is mechanical.

---

## Overview

Each MCP tool is a single Go file in `server/`. It exports one constructor:

```
func New<Name>Tool(client *ObsidianClient) (mcp.Tool, handler)
```

The `MCPServer` in `pkg/obsidianmcp/server.go` calls the constructor in `setupTools()`, which obtains the client from `s.obsidian`, wraps the handler with analytics, and registers both.

---

## Before you add a tool

Ask whether the tool needs to exist at all.

Prefer a small set of composable primitives (search, read, list) over a large set of endpoint-specific shortcuts. Each shortcut tool you add relieves the agent of having to reason — it gets an answer handed to it rather than working one out. Over many interactions that compounds: agents with fewer tools develop sharper retrieval strategies.

Concrete cases to skip:
- **Single well-known resource** — "get today's note", "get the active document". The agent can search or list to find these.
- **Aggregations derivable from reads** — "list all tags", "summarise recent activity". If the data is in the documents, search and read are sufficient.
- **UI side-effects** — "open this note in the editor". Side-effects don't belong in a read-only information layer unless the product explicitly requires them.

If the new tool makes the agent smarter by giving it access to data it genuinely cannot reach via existing tools, add it. If it just saves the agent one search call, skip it.

---

## Step 1 — Create the tool file

Create `server/<name>.go`. Minimal skeleton:

```go
package server

import (
    "context"
    "fmt"
    "strings"

    "github.com/mark3labs/mcp-go/mcp"
)

func NewMyTool(client *ObsidianClient) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("obsidian_my_tool",
        mcp.WithDescription("One sentence visible in the MCP tool list."),
        mcp.WithString("path", mcp.Required(), mcp.Description("Vault-relative file path.")),
    )
    tool.Annotations = mcp.ToolAnnotation{
        ReadOnlyHint:    true,   // false for any write operation
        DestructiveHint: false,  // true for DELETE
        IdempotentHint:  true,   // false if repeated calls can observe different results
        OpenWorldHint:   false,
    }

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        path, _ := req.Params.Arguments["path"].(string)
        if strings.TrimSpace(path) == "" {
            return mcp.NewToolResultError("'path' is required"), nil
        }

        data, status, err := client.Get(ctx, "/vault/"+strings.TrimPrefix(path, "/"))
        if err != nil {
            return mcp.NewToolResultError(fmt.Sprintf("Obsidian API unavailable: %v", err)), nil
        }
        if status == 404 {
            return mcp.NewToolResultError(fmt.Sprintf("%q not found in vault", path)), nil
        }
        if status != 200 {
            return mcp.NewToolResultError(fmt.Sprintf("Obsidian API returned %d: %s", status, string(data))), nil
        }

        return mcp.NewToolResultText(string(data)), nil
    }

    return tool, handler
}
```

### Tools without parameters

When a tool has no input, omit all `mcp.WithString` calls and skip parameter validation:

```go
func NewMyNoParamTool(client *ObsidianClient) (mcp.Tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
    tool := mcp.NewTool("obsidian_my_tool",
        mcp.WithDescription("Returns something from Obsidian — no parameters needed."),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: false, OpenWorldHint: false}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        data, status, err := client.Get(ctx, "/some/endpoint/")
        if err != nil {
            return mcp.NewToolResultError(fmt.Sprintf("Obsidian API unavailable: %v", err)), nil
        }
        if status == 404 {
            return mcp.NewToolResultError("endpoint not found"), nil
        }
        if status != 200 {
            return mcp.NewToolResultError(fmt.Sprintf("Obsidian API returned %d", status)), nil
        }
        return mcp.NewToolResultText(string(data)), nil
    }

    return tool, handler
}
```

---

## Step 2 — Register in setupTools()

Open `pkg/obsidianmcp/server.go`. The `client` variable at the top of `setupTools()` is `s.obsidian`. Add three lines for your tool:

```go
func (s *MCPServer) setupTools() error {
    client := s.obsidian   // already present — all tools share this client

    // ... existing tools ...

    myTool, myHandler := mcpserver.NewMyTool(client)
    s.mcpServer.AddTool(myTool, mcpserver.WithAnalytics("obsidian_my_tool", myHandler))
    s.tools = append(s.tools, myTool)

    return nil
}
```

Use `WithSearchAnalytics` instead of `WithAnalytics` for search tools (it logs the query parameter as a separate record type in the analytics log).

---

## ObsidianClient methods

`server/obsidian_client.go` exposes:

| Method | Signature | Use for |
|--------|-----------|---------|
| `Get` | `(ctx, path) ([]byte, int, error)` | GET endpoints |
| `Post` | `(ctx, path, body io.Reader, contentType string) ([]byte, int, error)` | POST endpoints |

### Path conventions

- All Obsidian REST API paths use a **trailing slash**: `/vault/`, `/active/`, `/tags/`, `/periodic/daily/`.
- Strip leading slashes from caller-supplied paths before appending: `strings.TrimPrefix(path, "/")`.
- Directory listing paths need a trailing slash: `/vault/` + path + `/`.

### POST with no body

Some endpoints are POST but carry their input in the URL (query string or path), not the body. Pass `nil, ""` for body and content type:

```go
// search uses POST but carries the query in the URL
endpoint := "/search/simple/?" + url.Values{"query": {q}}.Encode()
data, status, err := client.Post(ctx, endpoint, nil, "")
```

### Adding write methods (PUT, PATCH, DELETE)

To support write operations, add a method to `ObsidianClient` in `server/obsidian_client.go`. Mirror the structure of `do()` — build the request, set `Authorization` and `Content-Type`, then call `c.http.Do(req)` directly:

```go
func (c *ObsidianClient) Patch(ctx context.Context, path string, body io.Reader, contentType string, headers map[string]string) ([]byte, int, error) {
    req, err := http.NewRequestWithContext(ctx, "PATCH", c.BaseURL+path, body)
    if err != nil {
        return nil, 0, fmt.Errorf("build request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+c.APIKey)
    if contentType != "" {
        req.Header.Set("Content-Type", contentType)
    }
    for k, v := range headers {
        req.Header.Set(k, v)
    }
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, 0, fmt.Errorf("http: %w", err)
    }
    defer resp.Body.Close()
    data, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
    }
    return data, resp.StatusCode, nil
}
```

---

## Parameter patterns

**Required string parameter:**
```go
mcp.WithString("path", mcp.Required(), mcp.Description("Vault-relative path."))
```

**Optional string parameter:**
```go
mcp.WithString("path", mcp.Description("Subdirectory; omit for vault root."))
```

**Enum-style validation (no mcp.Enum helper — validate in handler):**
```go
switch period {
case "daily", "weekly", "monthly", "quarterly", "yearly":
default:
    return mcp.NewToolResultError("'period' must be one of: daily, weekly, monthly, quarterly, yearly"), nil
}
```

**Query string parameters:**
```go
import "net/url"

endpoint := "/search/simple/?" + url.Values{"query": {q}}.Encode()
```

---

## JSON response parsing

**Named type (reusable across tools):**
```go
type vaultListResponse struct {
    Files []string `json:"files"`
}
var resp vaultListResponse
if err := json.Unmarshal(data, &resp); err != nil {
    return mcp.NewToolResultError("failed to parse vault listing"), nil
}
```

**Inline struct (one-off, keeps the type next to the usage):**
```go
var resp struct {
    Tags []struct {
        Name  string `json:"name"`
        Count int    `json:"count"`
    } `json:"tags"`
}
if err := json.Unmarshal(data, &resp); err != nil {
    return mcp.NewToolResultText(string(data)), nil  // fall back to raw JSON
}
```

---

## PATCH (surgical edit) tools

The Obsidian REST API PATCH endpoint uses request headers to target a specific section:

| Header | Values |
|--------|--------|
| `Target-Type` | `heading`, `block`, `frontmatter` |
| `Target` | heading text, block ref ID, or frontmatter key |
| `Operation` | `append`, `prepend`, `replace` |
| `Content-Type` | `text/markdown` |

Tool parameters map directly:

```go
mcp.WithString("target_type", mcp.Required(), mcp.Description("heading, block, or frontmatter"))
mcp.WithString("target",      mcp.Required(), mcp.Description("Heading text, block ID, or frontmatter key"))
mcp.WithString("operation",   mcp.Required(), mcp.Description("append, prepend, or replace"))
mcp.WithString("content",     mcp.Required(), mcp.Description("Markdown content to insert"))
```

In the handler, pass the headers when calling the client:

```go
headers := map[string]string{
    "Target-Type": targetType,
    "Target":      target,
    "Operation":   operation,
}
body := strings.NewReader(content)
data, status, err := client.Patch(ctx, "/vault/"+strings.TrimPrefix(path, "/"), body, "text/markdown", headers)
```

Set `ReadOnlyHint: false` and `DestructiveHint: false` (PATCH is surgical, not destructive). Set `IdempotentHint: false` for `append`/`prepend`.

---

## Write operations — success codes

POST and PATCH endpoints that don't return a body respond with **204 No Content**. Accept both 200 and 204:

```go
if status != 200 && status != 204 {
    return mcp.NewToolResultError(fmt.Sprintf("Obsidian API returned %d", status)), nil
}
return mcp.NewToolResultText(fmt.Sprintf("Opened %q in Obsidian.", path)), nil
```

When you don't need the response body, discard it with `_`:

```go
_, status, err := client.Post(ctx, "/open/"+path, nil, "")
```

---

## Annotation reference

| Field | Meaning | Typical value |
|-------|---------|---------------|
| `ReadOnlyHint` | Tool does not modify anything | `true` for GET, `false` for PUT/PATCH/POST/DELETE |
| `DestructiveHint` | Tool may destroy data that cannot be recovered | `true` for DELETE |
| `IdempotentHint` | Calling twice has the same effect as calling once | `false` for append/prepend, and for endpoints that reflect live state (active note, periodic note) |
| `OpenWorldHint` | Tool may contact the internet | `false` — all calls go to localhost |

---

## Error handling conventions

- Return `mcp.NewToolResultError(msg)` for user-facing errors (not `error`). The `error` return is reserved for internal/framework failures.
- Always check `err` from `client.Get`/`client.Post` before checking `status`.
- Map 404 to a specific "not found" message; for all other non-200 statuses include the response body to surface the Obsidian error detail: `fmt.Sprintf("Obsidian API returned %d: %s", status, string(data))`.
- Never expose raw stack traces in tool results.

---

## Checklist

- [ ] `server/<name>.go` created with `New<Name>Tool(client *ObsidianClient)` constructor
- [ ] Tool name prefixed `obsidian_` (matches analytics key)
- [ ] `"strings"` imported if handler uses `strings.TrimSpace` or `strings.TrimPrefix`
- [ ] `tool.Annotations` set accurately
- [ ] Parameters validated before the HTTP call
- [ ] `err` checked, then `status`, then JSON parsed
- [ ] Non-200/non-204 errors include `string(data)` for Obsidian error detail
- [ ] Three lines added to `setupTools()` in `pkg/obsidianmcp/server.go`
- [ ] `go build -o obsidian-mcp.exe ./cmd/obsidian-mcp` passes
