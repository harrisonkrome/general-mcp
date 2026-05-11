package generalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	mcpserver "general-mcp/server"
)

// ServerConfig holds startup configuration.
type ServerConfig struct {
	HTTPMode bool
	Port     string
}

// MCPServer is the top-level server instance.
type MCPServer struct {
	config         ServerConfig
	mcpServer      *server.MCPServer
	sessionManager *SessionManager
	prompts        []mcp.Prompt
	tools          []mcp.Tool
	promptHandlers map[string]PromptHandler
}

// NewServer initialises the server: logger, analytics, tools, prompts.
func NewServer(config ServerConfig) (*MCPServer, error) {
	exeDir := getExeDir()

	if err := mcpserver.InitLogger(exeDir); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: logger init failed: %v\n", err)
	}

	analyticsFile := filepath.Join(exeDir, "general-mcp-analytics.json")
	mcpserver.InitializeAnalytics(analyticsFile)

	if config.Port == "" {
		config.Port = "8080"
	}

	mcpSrv := server.NewMCPServer("general-mcp", "0.1.0",
		server.WithPromptCapabilities(true),
	)

	s := &MCPServer{
		config:    config,
		mcpServer: mcpSrv,
		sessionManager: &SessionManager{
			sessions: make(map[string]*SessionContext),
		},
		prompts:        []mcp.Prompt{},
		tools:          []mcp.Tool{},
		promptHandlers: make(map[string]PromptHandler),
	}

	if err := s.setupTools(); err != nil {
		return nil, fmt.Errorf("setupTools: %w", err)
	}
	if err := s.setupPrompts(); err != nil {
		return nil, fmt.Errorf("setupPrompts: %w", err)
	}

	if _, err := s.sessionManager.InjectPrompt("default", "general_rules", s.promptHandlers); err != nil {
		mcpserver.WriteDebugLog("Failed to auto-inject general_rules: %v\n", err)
	} else {
		mcpserver.WriteDebugLog("Auto-injected general_rules into default session\n")
	}

	return s, nil
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

func (s *MCPServer) setupTools() error {
	s.addPlaceholderTool()
	return nil
}

// addPlaceholderTool registers general_placeholder.
// Replace this with your actual tool implementation.
func (s *MCPServer) addPlaceholderTool() {
	tool := mcp.NewTool("general_placeholder",
		mcp.WithDescription("Placeholder tool — replace with your implementation."),
		mcp.WithString("input", mcp.Required(), mcp.Description("Input value.")),
	)
	tool.Annotations = mcp.ToolAnnotation{ReadOnlyHint: true, IdempotentHint: true}

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, _ := req.Params.Arguments["input"].(string)
		if input == "" {
			return mcp.NewToolResultError("'input' is required"), nil
		}
		return mcp.NewToolResultText("placeholder: " + input), nil
	}

	s.mcpServer.AddTool(tool, mcpserver.WithAnalytics("general_placeholder", handler))
	s.tools = append(s.tools, tool)
}

// ---------------------------------------------------------------------------
// Prompt registration
// ---------------------------------------------------------------------------

func (s *MCPServer) setupPrompts() error {
	rulesHandler := func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		content := `You are an AI assistant with access to tools provided by the general-mcp server.
Use the available tools to retrieve authoritative content before generating answers,
rather than relying solely on training data.

## General rules

1. Retrieve context first. Before answering domain questions, use the available
   tools to pull relevant source material.
2. Cite what you read. When summarising retrieved content, attribute the source
   so the user can verify.
3. Keep responses concise. Return high-signal excerpts rather than dumping entire
   retrieved documents unless the user explicitly asks for it.
`
		return mcp.NewGetPromptResult(
			"General MCP Rules and Guidelines",
			[]mcp.PromptMessage{
				mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(content)),
			},
		), nil
	}

	prompt := mcp.NewPrompt("general_rules",
		mcp.WithPromptDescription("Rules and guidelines for effective use of the general-mcp server tools."))

	s.prompts = append(s.prompts, prompt)
	s.promptHandlers["general_rules"] = rulesHandler
	s.mcpServer.AddPrompt(prompt, rulesHandler)

	return nil
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// ServeStdio runs the server over stdin/stdout with graceful shutdown.
func (s *MCPServer) ServeStdio() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- server.ServeStdio(s.mcpServer) }()
	fmt.Fprintf(os.Stderr, "Listening for messages on standard input...\n")

	select {
	case err := <-done:
		return err
	case <-sigChan:
		mcpserver.WriteDebugLog("Shutdown signal received\n")
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return fmt.Errorf("shutdown timeout")
		}
	}
}

// ServeHTTP runs the server over SSE with additional REST endpoints.
func (s *MCPServer) ServeHTTP() error {
	sseServer := server.NewSSEServer(s.mcpServer)

	mux := http.NewServeMux()
	mux.Handle("/sse", sseServer)
	mux.Handle("/message", sseServer)

	cors := func(w http.ResponseWriter) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}

	mux.HandleFunc("/tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cors(w)
		if r.Method == "OPTIONS" {
			return
		}
		var list []map[string]string
		for _, t := range s.tools {
			list = append(list, map[string]string{"name": t.Name, "description": t.Description})
		}
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/prompts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cors(w)
		if r.Method == "OPTIONS" {
			return
		}
		var list []PromptInfo
		for _, p := range s.prompts {
			list = append(list, PromptInfo{Name: p.Name, Description: p.Description})
		}
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/session/context", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cors(w)
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req InjectPromptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.SessionID == "" {
			req.SessionID = "default"
		}
		if req.PromptName == "" {
			http.Error(w, "prompt_name required", http.StatusBadRequest)
			return
		}
		resp, err := s.sessionManager.InjectPrompt(req.SessionID, req.PromptName, s.promptHandlers)
		if err != nil {
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cors(w)
		if r.Method == "OPTIONS" {
			return
		}
		sessionID := strings.TrimPrefix(r.URL.Path, "/session/")
		if sessionID == "" {
			http.Error(w, "Session ID required", http.StatusBadRequest)
			return
		}
		sess := s.sessionManager.GetOrCreateSession(sessionID)
		json.NewEncoder(w).Encode(sess)
	})

	mux.HandleFunc("/analytics/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cors(w)
		if r.Method == "OPTIONS" {
			return
		}
		json.NewEncoder(w).Encode(mcpserver.GetAnalyticsSummary())
	})

	addr := ":" + s.config.Port
	mcpserver.WriteDebugLog("HTTP server starting on %s\n", addr)
	fmt.Fprintf(os.Stderr, "Server listening on http://localhost%s\n", addr)
	return http.ListenAndServe(addr, mux)
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

func (s *MCPServer) GetTools() []mcp.Tool             { return s.tools }
func (s *MCPServer) GetPrompts() []mcp.Prompt          { return s.prompts }
func (s *MCPServer) GetSessionManager() *SessionManager { return s.sessionManager }
func (s *MCPServer) PrintStartupInfo()                 { printStartupInfo(s.prompts, s.tools) }

