# INITIAL.md — Skeleton Origin Record

This document records what was built, why each decision was made, and what the
project is designed to grow into. It is written once and not updated as the
project evolves.

---

## Purpose

`general-mcp` is a **baseline Go MCP server** whose sole job is to retrieve
high-signal text and surface it to AI agents over the
[Model Context Protocol](https://modelcontextprotocol.io). It is not a
product-specific server. It is the starting point from which product-specific
servers are built by adding tools and replacing the `general_` prefix with a
domain name.

The three canonical retrieval patterns it demonstrates are:

1. **REST** — HTTP GET a URL and return the response body
2. **Filesystem** — read or enumerate local files
3. **Database** — execute a read-only SQL query against a SQLite file

---

## Prior Art

The skeleton was derived by reading the full source of two production MCP
servers in sibling directories:

| Server | Location | What it taught |
|--------|----------|----------------|
| `nsoftware-mcp` | `../nsoftware-mcp` | Inline tool registration, SQLite DB discovery, contract loading, analytics middleware, stdio/HTTP transport split, session injection pattern |
| `xmlui-mcp` | `../xmlui-mcp` | Factory-function tool creation, versioned repo caching, update-check prompt, `WithSearchAnalytics` extension of the base middleware |

Both servers share the same `github.com/mark3labs/mcp-go v0.26.0` MCP library,
the same `modernc.org/sqlite` driver, identical `SessionManager` logic, and
identical transport code. `general-mcp` extracts that common substrate and
leaves the domain-specific layers as clearly marked extension points.

---

## Repository Layout

```
general-mcp/
├── cmd/
│   └── general-mcp/
│       └── main.go                 CLI entry point
├── pkg/
│   └── generalmcp/
│       ├── server.go               MCPServer struct + all tools + transports
│       ├── session.go              SessionManager
│       ├── types.go                Shared types
│       └── utils.go                getExeDir, printStartupInfo
├── server/
│   ├── analytics.go                Middleware + NDJSON log
│   └── logger.go                   File-based debug log
├── go.mod
├── go.sum
├── CLAUDE.md                       Build/architecture guide for Claude Code
└── INITIAL.md                      This file
```

---

## Module

```
module general-mcp
go     1.24.0
```

**Direct dependencies:**

| Dependency | Version | Role |
|------------|---------|------|
| `github.com/mark3labs/mcp-go` | v0.26.0 | MCP protocol — tool/prompt registration, stdio server, SSE server |
| `modernc.org/sqlite` | v1.46.1 | Pure-Go SQLite driver (no CGo required) |

All other entries in `go.mod` are transitive. The dependency set is identical
to `nsoftware-mcp`, so on a machine that has already built that project every
module is already in the local cache.

---

## Source Files

### `cmd/general-mcp/main.go`

Parses two flags (`-http`, `-port`), constructs a `ServerConfig`, calls
`generalmcp.NewServer`, prints startup JSON to stderr, then calls either
`ServeStdio` or `ServeHTTP`. Identical in structure to both reference servers.

### `pkg/generalmcp/types.go`

Defines all shared types:

- `PromptHandler` — `func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error)`
- `SessionContext` — per-session state: ID, injected prompt names, last-activity timestamp, accumulated `[]mcp.PromptMessage`
- `SessionManager` — `map[string]*SessionContext` guarded by `sync.RWMutex`
- `InjectPromptRequest` / `InjectPromptResponse` — JSON bodies for the HTTP `/session/context` endpoint
- `PromptInfo`, `ToolInfo`, `StartupInfo` — serialised to stderr at startup

### `pkg/generalmcp/session.go`

Implements `SessionManager` methods:

- `GetOrCreateSession(id)` — upsert with activity timestamp update
- `InjectPrompt(sessionID, promptName, handlers)` — idempotent; appends prompt messages into `SessionContext.Context`
- `GetSession`, `RemoveSession`, `ListSessions` — read/write accessors
- `CleanupInactiveSessions(maxAge)` — purge idle sessions

This is a direct port of the identical logic in both reference servers.

### `pkg/generalmcp/utils.go`

- `getExeDir()` — resolves the directory of the running binary via `os.Executable()`, falling back to `os.Getwd()`; used to locate runtime files (log, analytics) next to the binary regardless of working directory
- `printStartupInfo(prompts, tools)` — marshals `StartupInfo` as indented JSON to stderr on startup; consumed by MCP Inspector and debugging scripts

### `server/logger.go`

Global file-based logger initialised by `InitLogger(logDir)`. Writes to
`general-mcp-server.log` in `logDir`. Thread-safe via `sync.RWMutex`.
`WriteDebugLog(format, args...)` is the primary write surface used throughout
`server.go`. Falls back to stderr if the log file cannot be opened.

### `server/analytics.go`

NDJSON invocation log written to `general-mcp-analytics.json`.

Two middleware wrappers:

- **`WithAnalytics(toolName, handler)`** — wraps any tool handler; records
  `ToolInvocation{type, timestamp, tool_name, arguments, success,
  result_size_chars, error_msg}` after every call
- **`WithSearchAnalytics(toolName, handler)`** — additionally records a
  `SearchQuery{type, timestamp, tool_name, query, result_count, success}`
  entry; intended for tools whose primary parameter is a free-text query

`GetAnalyticsSummary()` aggregates per-tool call counts and success rates;
served at the `/analytics/summary` REST endpoint in HTTP mode.

The analytics data is loaded from the NDJSON file at startup so historical
counts survive restarts.

### `pkg/generalmcp/server.go`

The heaviest file. Contains:

#### `ServerConfig`

```go
type ServerConfig struct {
    HTTPMode bool
    Port     string   // default "8080"
}
```

Extend with domain-specific fields (e.g. `DataDir`, `APIKey`, `DBPath`) as
needed for a concrete server.

#### `NewServer(config)`

Initialisation sequence:

1. Resolve exe directory — log and analytics files are written there
2. `mcpserver.InitLogger(exeDir)` — open `general-mcp-server.log`
3. `mcpserver.InitializeAnalytics(analyticsFile)` — load/create `general-mcp-analytics.json`
4. `server.NewMCPServer("general-mcp", "0.1.0", server.WithPromptCapabilities(true))`
5. Construct `MCPServer` struct
6. `setupTools()` — register all 8 tools
7. `setupPrompts()` — register `general_rules`
8. Auto-inject `general_rules` into the `"default"` session

#### Tools registered

| Tool name | Function | Key parameters |
|-----------|----------|----------------|
| `general_fetch_url` | `addFetchURLTool()` | `url` (required), `accept` |
| `general_read_file` | `addReadFileTool()` | `path` (required) |
| `general_list_files` | `addListFilesTool()` | `directory` (required), `pattern`, `recursive` |
| `general_query_db` | `addQueryDBTool()` | `db_path` (required), `query` (required), `limit` |
| `general_inject_prompt` | `addInjectPromptTool()` | `prompt_name`, `session_id` |
| `general_list_prompts` | `addListPromptsTool()` | — |
| `general_get_prompt` | `addGetPromptTool()` | `prompt_name` (required) |
| `general_get_session` | `addGetSessionTool()` | `session_id` |

All tools are annotated `ReadOnlyHint: true, IdempotentHint: true` where
appropriate. All are wrapped with `mcpserver.WithAnalytics(...)`.

#### Data-source helpers (private functions)

**`fetchURL(ctx, urlStr, accept) (string, error)`**

- 30-second timeout via `http.Client`
- `User-Agent: general-mcp/1.0`
- Returns non-2xx as an error with the status code and text
- Context-aware via `http.NewRequestWithContext`

**`listFiles(dir, pattern string, recursive bool) ([]string, error)`**

- Non-recursive path: `os.ReadDir` + `filepath.Match` on each entry name
- Recursive path: `filepath.WalkDir` + `filepath.Match` on `filepath.Base(path)`
- Returns paths relative to `dir`

**`queryDB(ctx, dbPath, query string, maxRows int) (string, error)`**

- Rejects any statement whose trimmed, uppercased prefix is not `SELECT`
- Opens the database with `sql.Open("sqlite", dbPath)` using the
  `modernc.org/sqlite` driver registered via blank import
- Formats results as a GitHub-flavoured markdown table
- Truncates at `maxRows` (default 100) with a note appended to the output
- Context-cancellable via `db.QueryContext`

#### `ServeStdio()`

Runs `server.ServeStdio(s.mcpServer)` in a goroutine. A `signal.Notify` on
`SIGINT`/`SIGTERM` initiates graceful shutdown with a 5-second timeout. Used by
Claude Desktop, Cursor, VS Code, and any MCP-aware IDE.

#### `ServeHTTP()`

Creates an `server.NewSSEServer` and mounts it alongside REST endpoints on a
plain `http.ServeMux`:

| Path | Method | Purpose |
|------|--------|---------|
| `/sse` | GET | MCP SSE stream |
| `/message` | POST | MCP message endpoint |
| `/tools` | GET | JSON list of tool names and descriptions |
| `/prompts` | GET | JSON list of prompt names and descriptions |
| `/session/{id}` | GET | Serialised `SessionContext` |
| `/session/context` | POST | `InjectPromptRequest` → `InjectPromptResponse` |
| `/analytics/summary` | GET | Aggregated invocation counts and success rates |

All endpoints set `Access-Control-Allow-Origin: *` and handle `OPTIONS`
preflight for browser access. Useful for dashboards, the MCP Inspector, and
integration testing.

---

## Design Decisions

**Single `server.go` rather than factory files.** Both reference servers offer
two organisational models: `nsoftware-mcp` keeps all tools inline in one file;
`xmlui-mcp` uses per-tool factory functions in the `server/` package. With four
tools a single file is less overhead. Once a concrete server has 8+ tools,
splitting into factory files (one per tool or per domain) is straightforward.

**No startup data-discovery.** Unlike `nsoftware-mcp` (which scans `dbs/` and
`contracts/` at startup), this skeleton performs no directory scanning.
Concrete servers add their own discovery logic in `NewServer` before calling
`setupTools()`, storing discovered handles (e.g. `map[string]*sql.DB`) on
`MCPServer`.

**SQLite included from the start.** `modernc.org/sqlite` is a direct dependency
rather than a stub, for two reasons: (1) it is pure Go with no CGo, so it adds
no build complexity on Windows; (2) the database query use-case is explicit in
the project brief and a non-compiling stub provides no value as a reference.

**`general_` tool prefix.** All tool names use the `general_` prefix as a
clear marker of origin. When building a concrete server, rename the module,
rename the package, and do a project-wide search-and-replace of `general_` with
the domain prefix (e.g. `acme_`). The rules prompt should be updated similarly.

**`general_rules` prompt auto-injected.** Following the pattern from both
reference servers, the rules prompt is pushed into the `"default"` session
during `NewServer`. This ensures the agent receives context about tool usage
intent even before the first explicit tool call.

---

## What Is Intentionally Absent

The following were considered and deliberately left out of the skeleton:

- **Connection pooling** — `queryDB` opens and closes a `*sql.DB` per call.
  A concrete server that queries the same database on every request should open
  the DB once in `NewServer` and store it on `MCPServer`.
- **Authentication** — `fetch_url` sends no auth headers. API keys and bearer
  tokens belong in `ServerConfig` and should be read from environment variables,
  not hard-coded.
- **Path allow-listing** — `read_file` and `list_files` accept arbitrary paths.
  A concrete server should validate paths against a configured root or allow-list.
- **Update checking** — `xmlui-mcp` polls GitHub for a newer CLI release and
  prepends a notice to search results. This is product-specific behaviour not
  appropriate for a generic skeleton.
- **Versioned data caching** — `xmlui-mcp` downloads and caches versioned
  release ZIPs. This is specific to projects that wrap a versioned external
  artifact.
- **Tests** — neither reference server ships automated tests for its MCP layer.
  The MCP Inspector (`npx @modelcontextprotocol/inspector`) is the primary
  manual testing surface.
