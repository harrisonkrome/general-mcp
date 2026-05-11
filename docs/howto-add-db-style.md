# How to Add SQLite Database-Backed Tools

This guide describes every change needed to take this skeleton from a static placeholder to a server whose tools query per-resource SQLite databases discovered at startup.

The reference implementation is in `../nsoftware-mcp`. The steps below reproduce that pattern in this project.

---

## Before you add a tool

Ask whether the tool needs to exist at all.

Prefer a small set of composable primitives (search, read, list) over a large set of query-specific shortcuts. Each shortcut tool you add relieves the agent of having to reason — it gets an answer handed to it rather than working one out. Over many interactions that compounds: agents with fewer tools develop sharper retrieval strategies.

Concrete cases to skip:
- **Single-record lookup** — "get item by ID", "get the latest entry". The agent can list or search and filter.
- **Aggregations derivable from reads** — "count items by category", "summarise recent activity". If the data is queryable via a search/list primitive, an extra aggregation tool adds a crutch.
- **Filtered list variants** — separate tools for "list active items" vs. "list archived items". Use an optional filter parameter on the main list tool instead.

If the new tool gives the agent access to data it genuinely cannot reach via existing tools, add it. If it just saves the agent one query call, skip it.

---

## Overview

The pattern works like this:

1. At startup, scan a `dbs/` directory next to the executable for SQLite files.
2. Open and store each database in a `map[string]*DB` on `MCPServer`.
3. In each tool handler, look up the right `*DB` by a parameter (e.g. a product name, tenant ID, or catalog key) and run the query.

One database per logical resource; tool handlers are just thin wrappers around typed query methods.

---

## Step 1 — Create `pkg/<yourpkg>/db.go`

This file owns the `DB` struct, the row types, and all SQL. Nothing in this file imports MCP.

```go
package <yourpkg>

import (
    "database/sql"
    "fmt"
    "os"
    "path/filepath"

    _ "modernc.org/sqlite"
)

// DB wraps a single SQLite connection for one resource database.
type DB struct {
    conn *sql.DB
}

// --- Row types (one per table you query) ---

type Catalog struct {
    ID   string
    Name string
    Desc string
}

// OpenDB opens and pings a SQLite file at dbPath.
func OpenDB(dbPath string) (*DB, error) {
    conn, err := sql.Open("sqlite", dbPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open database: %w", err)
    }
    if err := conn.Ping(); err != nil {
        conn.Close()
        return nil, fmt.Errorf("failed to connect to database: %w", err)
    }
    return &DB{conn: conn}, nil
}

// Close releases the connection.
func (db *DB) Close() error {
    return db.conn.Close()
}

// DiscoverDatabases scans dbsDir for subdirectories matching the layout:
//   <dbsDir>/<ResourceName>/data/data.sqlite
// Returns a map of resource name → opened DB.
func DiscoverDatabases(dbsDir string) (map[string]*DB, error) {
    databases := make(map[string]*DB)

    entries, err := os.ReadDir(dbsDir)
    if err != nil {
        if os.IsNotExist(err) {
            return databases, nil // empty is fine; tools return "unknown resource"
        }
        return nil, fmt.Errorf("failed to read dbs directory: %w", err)
    }

    for _, entry := range entries {
        if !entry.IsDir() {
            continue
        }
        dbPath := filepath.Join(dbsDir, entry.Name(), "data", "data.sqlite")
        if _, err := os.Stat(dbPath); err != nil {
            continue
        }
        db, err := OpenDB(dbPath)
        if err != nil {
            fmt.Fprintf(os.Stderr, "WARNING: failed to open database for %s: %v\n", entry.Name(), err)
            continue // skip bad databases; don't crash startup
        }
        databases[entry.Name()] = db
    }
    return databases, nil
}

// --- Query methods (add one per SQL operation you need) ---

// ListItems returns rows from the items table, optionally filtered by search term.
func (db *DB) ListItems(search string) ([]Catalog, error) {
    var (
        rows *sql.Rows
        err  error
    )
    if search == "" {
        rows, err = db.conn.Query(
            `SELECT id, COALESCE(name,''), COALESCE(desc,'') FROM items ORDER BY name`,
        )
    } else {
        pattern := "%" + search + "%"
        rows, err = db.conn.Query(
            `SELECT id, COALESCE(name,''), COALESCE(desc,'')
             FROM items WHERE name LIKE ? OR desc LIKE ?
             ORDER BY name`,
            pattern, pattern,
        )
    }
    if err != nil {
        return nil, fmt.Errorf("failed to query items: %w", err)
    }
    defer rows.Close()

    var items []Catalog
    for rows.Next() {
        var c Catalog
        if err := rows.Scan(&c.ID, &c.Name, &c.Desc); err != nil {
            return nil, fmt.Errorf("failed to scan item: %w", err)
        }
        items = append(items, c)
    }
    return items, rows.Err()
}
```

### Notes
- Use `COALESCE(col,'')` in every `SELECT` to avoid null pointer surprises when scanning into `string`.
- Use `COLLATE NOCASE` on `WHERE name = ?` equality lookups.
- `DiscoverDatabases` is non-fatal per entry: a missing or corrupt database logs a warning but doesn't stop the server.
- `modernc.org/sqlite` is already in `go.mod` (as an explicit dependency in this skeleton); just add the blank import.

---

## Step 2 — Add `databases` to `MCPServer`

Edit `pkg/<yourpkg>/server.go`. Add the field to the struct:

```go
// Before
type MCPServer struct {
    config         ServerConfig
    mcpServer      *server.MCPServer
    sessionManager *SessionManager
    prompts        []mcp.Prompt
    tools          []mcp.Tool
    promptHandlers map[string]PromptHandler
}

// After
type MCPServer struct {
    config         ServerConfig
    databases      map[string]*DB   // keyed by resource name, e.g. "Catalog", "Inventory"
    mcpServer      *server.MCPServer
    sessionManager *SessionManager
    prompts        []mcp.Prompt
    tools          []mcp.Tool
    promptHandlers map[string]PromptHandler
}
```

---

## Step 3 — Discover databases in `NewServer`

In `NewServer`, after the logger is initialized and before `setupTools` is called:

```go
// Discover databases from dbs/ directory next to the executable
dbsDir := filepath.Join(exeDir, "dbs")
databases, err := DiscoverDatabases(dbsDir)
if err != nil {
    mcpserver.WriteDebugLog("Warning: failed to discover databases in %s: %v\n", dbsDir, err)
}
mcpserver.WriteDebugLog("Discovered %d database(s) in %s\n", len(databases), dbsDir)
```

Then wire it into the struct literal:

```go
s := &MCPServer{
    config:         config,
    databases:      databases,   // <-- add this line
    mcpServer:      mcpServer,
    sessionManager: sessionManager,
    prompts:        []mcp.Prompt{},
    tools:          []mcp.Tool{},
    promptHandlers: make(map[string]PromptHandler),
}
```

`filepath` is already imported; no new imports needed in `server.go` for this step.

---

## Step 4 — Replace placeholder tool with DB-backed tools

In `setupTools`, remove the call to `s.addPlaceholderTool()` and replace it with your new tool methods. Here is a complete example for a "list items" tool:

```go
func (s *MCPServer) addListItemsTool() {
    tool := mcp.NewTool("catalog_list_items",
        mcp.WithDescription("List items in a named catalog. Returns id, name, and description for each item."),
        mcp.WithString("catalog", mcp.Required(), mcp.Description("Catalog name (matches a database in the dbs/ directory)")),
        mcp.WithString("search", mcp.Description("Optional keyword filter applied to name and description")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, IdempotentHint: true}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        catalog, _ := req.Params.Arguments["catalog"].(string)
        if catalog == "" {
            return mcp.NewToolResultError("'catalog' is required"), nil
        }
        search, _ := req.Params.Arguments["search"].(string)

        db, ok := s.databases[catalog]
        if !ok {
            return mcp.NewToolResultError(fmt.Sprintf("Unknown catalog '%s'. Available: %v", catalog, s.availableCatalogs())), nil
        }

        items, err := db.ListItems(search)
        if err != nil {
            return mcp.NewToolResultError(fmt.Sprintf("Database error: %v", err)), nil
        }
        if len(items) == 0 {
            return mcp.NewToolResultText("No items found."), nil
        }

        var sb strings.Builder
        for _, item := range items {
            fmt.Fprintf(&sb, "- **%s** (%s): %s\n", item.Name, item.ID, item.Desc)
        }
        return mcp.NewToolResultText(sb.String()), nil
    }

    s.mcpServer.AddTool(tool, mcpserver.WithAnalytics("catalog_list_items", handler))
    s.tools = append(s.tools, tool)
}

// availableCatalogs returns sorted catalog names for error messages.
func (s *MCPServer) availableCatalogs() []string {
    names := make([]string, 0, len(s.databases))
    for k := range s.databases {
        names = append(names, k)
    }
    sort.Strings(names)
    return names
}
```

Call it from `setupTools`:

```go
func (s *MCPServer) setupTools() error {
    s.addListItemsTool()
    // s.addGetItemTool()  // add more as needed
    // ... session/prompt tools remain unchanged ...
    return nil
}
```

Add `"sort"` and `"strings"` to the import block in `server.go` if not already present.

---

## Step 5 — Provide the databases

At runtime the server expects this layout next to the executable:

```
dbs/
  CatalogA/
    data/
      data.sqlite
  CatalogB/
    data/
      data.sqlite
```

The subdirectory name (`CatalogA`) is the key used in the `databases` map and the value the agent passes as the `catalog` parameter. During development you can create a test database with:

```powershell
# requires sqlite3 CLI
New-Item -ItemType Directory -Path dbs\TestCatalog\data -Force
sqlite3 dbs\TestCatalog\data\data.sqlite "
  CREATE TABLE items (id TEXT, name TEXT, desc TEXT);
  INSERT INTO items VALUES ('1','Widget','A small widget');
"
```

---

## Summary of files changed

| File | Change |
|------|--------|
| `pkg/<yourpkg>/db.go` | **New.** `DB`, row types, `OpenDB`, `Close`, `DiscoverDatabases`, query methods |
| `pkg/<yourpkg>/server.go` | Add `databases map[string]*DB` field; call `DiscoverDatabases` in `NewServer`; replace placeholder tool with DB-backed tool methods |
| `dbs/<ResourceName>/data/data.sqlite` | **New runtime asset.** One SQLite file per logical resource |

`go.mod` and `go.sum` require no changes — `modernc.org/sqlite` is already present.
