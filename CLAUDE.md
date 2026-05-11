# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build

```powershell
go build -o general-mcp.exe ./cmd/general-mcp
```

Manual testing via the MCP Inspector:

```
npx @modelcontextprotocol/inspector
```

## Architecture

**general-mcp** is a skeleton Go MCP server. It ships one placeholder tool to replace with your implementation:

| Tool | Description |
|------|-------------|
| `general_placeholder` | Echo placeholder — replace with your actual tool |

### Package layout

```
cmd/general-mcp/main.go          CLI entry point — flag parsing, serve stdio or HTTP
pkg/generalmcp/server.go         MCPServer struct, NewServer, setupTools, setupPrompts, ServeStdio, ServeHTTP
pkg/generalmcp/session.go        SessionManager — thread-safe prompt injection per session
pkg/generalmcp/types.go          Shared types (SessionContext, PromptHandler, etc.)
pkg/generalmcp/utils.go          getExeDir, printStartupInfo
server/analytics.go              WithAnalytics / WithSearchAnalytics middleware + NDJSON log
server/logger.go                 File-based debug log (general-mcp-server.log)
```

### Transport modes

- **Stdio** (default): used by Claude Desktop, Cursor, VS Code MCP extensions
- **HTTP** (`-http -port 8080`): SSE at `/sse` + `/message`; REST at `/tools`, `/prompts`, `/session/<id>`, `/session/context`, `/analytics/summary`

### Tool registration pattern

All tools are registered in `setupTools()` in `pkg/generalmcp/server.go`. Each `add*Tool()` helper follows this pattern:

```go
func (s *MCPServer) addMyTool() {
    tool := mcp.NewTool("general_my_tool",
        mcp.WithDescription("..."),
        mcp.WithString("param", mcp.Required(), mcp.Description("...")),
    )
    tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, IdempotentHint: true}

    handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        val, _ := req.Params.Arguments["param"].(string)
        if val == "" {
            return mcp.NewToolResultError("'param' is required"), nil
        }
        // ... do work ...
        return mcp.NewToolResultText(result), nil
    }

    s.mcpServer.AddTool(tool, mcpserver.WithAnalytics("general_my_tool", handler))
    s.tools = append(s.tools, tool)
}
```

Call `s.addMyTool()` from `setupTools()` to register it.

### Adding a tool

Copy `addPlaceholderTool()` in `pkg/generalmcp/server.go`, rename it, and call it from `setupTools()`. The pattern:

1. Define the tool with `mcp.NewTool` and its parameters
2. Write a handler `func(ctx, req) (*mcp.CallToolResult, error)`
3. Register with `s.mcpServer.AddTool(tool, mcpserver.WithAnalytics("name", handler))`
4. Append to `s.tools` and call the method from `setupTools()`

### Runtime files (next to the executable)

| File | Purpose |
|------|---------|
| `general-mcp-server.log` | Debug log (`mcpserver.WriteDebugLog`) |
| `general-mcp-analytics.json` | NDJSON tool invocation log |
