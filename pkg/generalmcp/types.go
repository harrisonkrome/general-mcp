package generalmcp

import (
	"context"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// PromptHandler defines the signature for prompt handlers.
type PromptHandler func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error)

// PromptInfo represents basic prompt information.
type PromptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PromptContent represents full prompt content with messages.
type PromptContent struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Messages    []mcp.PromptMessage `json:"messages"`
}

// ToolInfo represents basic tool information.
type ToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// StartupInfo is printed as JSON to stderr on startup.
type StartupInfo struct {
	Type    string       `json:"type"`
	Prompts []PromptInfo `json:"prompts"`
	Tools   []ToolInfo   `json:"tools"`
}

// SessionContext holds per-session injected prompt state.
type SessionContext struct {
	ID              string              `json:"id"`
	InjectedPrompts []string            `json:"injected_prompts"`
	LastActivity    time.Time           `json:"last_activity"`
	Context         []mcp.PromptMessage `json:"context"`
}

// SessionManager manages multiple concurrent sessions.
type SessionManager struct {
	sessions map[string]*SessionContext
	mutex    sync.RWMutex
}

// InjectPromptRequest is the JSON body for the POST /session/context endpoint.
type InjectPromptRequest struct {
	SessionID  string `json:"session_id"`
	PromptName string `json:"prompt_name"`
}

// InjectPromptResponse is returned by InjectPrompt.
type InjectPromptResponse struct {
	Success bool                 `json:"success"`
	Message string               `json:"message"`
	Content *mcp.GetPromptResult `json:"content,omitempty"`
}
