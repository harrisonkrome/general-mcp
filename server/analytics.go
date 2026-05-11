package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

type ToolInvocation struct {
	Type       string                 `json:"type"`
	Timestamp  time.Time              `json:"timestamp"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments"`
	Success    bool                   `json:"success"`
	ResultSize int                    `json:"result_size_chars"`
	ErrorMsg   string                 `json:"error_msg,omitempty"`
}

type SearchQuery struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	ToolName    string    `json:"tool_name"`
	Query       string    `json:"query"`
	ResultCount int       `json:"result_count"`
	Success     bool      `json:"success"`
}

type AnalyticsData struct {
	ToolInvocations []ToolInvocation `json:"tool_invocations"`
	SearchQueries   []SearchQuery    `json:"search_queries"`
}

type Analytics struct {
	mu      sync.RWMutex
	data    AnalyticsData
	logFile string
}

var globalAnalytics *Analytics

func InitializeAnalytics(logFile string) {
	globalAnalytics = newAnalytics(logFile)
}

func newAnalytics(logFile string) *Analytics {
	a := &Analytics{
		data: AnalyticsData{
			ToolInvocations: make([]ToolInvocation, 0),
			SearchQueries:   make([]SearchQuery, 0),
		},
		logFile: logFile,
	}
	a.loadData()
	return a
}

func (a *Analytics) loadData() {
	if _, err := os.Stat(a.logFile); os.IsNotExist(err) {
		return
	}

	data, err := os.ReadFile(a.logFile)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		recordType, _ := raw["type"].(string)
		switch recordType {
		case "tool_invocation":
			var inv ToolInvocation
			if err := json.Unmarshal([]byte(line), &inv); err == nil {
				a.data.ToolInvocations = append(a.data.ToolInvocations, inv)
			}
		case "search_query":
			var sq SearchQuery
			if err := json.Unmarshal([]byte(line), &sq); err == nil {
				a.data.SearchQueries = append(a.data.SearchQueries, sq)
			}
		}
	}
}

func (a *Analytics) writeLine(data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}

	file, err := os.OpenFile(a.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer file.Close()

	file.Write(jsonData)
	file.Write([]byte("\n"))
	file.Sync()
}

func (a *Analytics) logToolInvocation(toolName string, args map[string]interface{}, success bool, resultSize int, errorMsg string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	inv := ToolInvocation{
		Type:       "tool_invocation",
		Timestamp:  time.Now(),
		ToolName:   toolName,
		Arguments:  args,
		Success:    success,
		ResultSize: resultSize,
		ErrorMsg:   errorMsg,
	}

	a.data.ToolInvocations = append(a.data.ToolInvocations, inv)
	a.writeLine(inv)
}

func (a *Analytics) logSearchQuery(toolName string, query string, resultCount int, success bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	sq := SearchQuery{
		Type:        "search_query",
		Timestamp:   time.Now(),
		ToolName:    toolName,
		Query:       query,
		ResultCount: resultCount,
		Success:     success,
	}

	a.data.SearchQueries = append(a.data.SearchQueries, sq)
	a.writeLine(sq)
}

func (a *Analytics) GetSummary() map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	toolCounts := make(map[string]int)
	successRates := make(map[string]float64)
	toolSuccesses := make(map[string]int)

	for _, inv := range a.data.ToolInvocations {
		toolCounts[inv.ToolName]++
		if inv.Success {
			toolSuccesses[inv.ToolName]++
		}
	}

	for tool, count := range toolCounts {
		successRates[tool] = float64(toolSuccesses[tool]) / float64(count) * 100
	}

	searchSuccessRate := 0.0
	if len(a.data.SearchQueries) > 0 {
		successCount := 0
		for _, sq := range a.data.SearchQueries {
			if sq.Success {
				successCount++
			}
		}
		searchSuccessRate = float64(successCount) / float64(len(a.data.SearchQueries)) * 100
	}

	return map[string]interface{}{
		"total_tool_invocations": len(a.data.ToolInvocations),
		"total_search_queries":   len(a.data.SearchQueries),
		"tool_usage_counts":      toolCounts,
		"tool_success_rates":     successRates,
		"search_success_rate":    searchSuccessRate,
	}
}

// WithAnalytics wraps a tool handler with analytics tracking.
func WithAnalytics(toolName string, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := handler(ctx, req)

		success := err == nil && result != nil
		resultSize := 0
		errorMsg := ""

		if result != nil {
			for _, content := range result.Content {
				switch c := content.(type) {
				case *mcp.TextContent:
					resultSize += len(c.Text)
				case mcp.TextContent:
					resultSize += len(c.Text)
				}
			}
		}

		if err != nil {
			errorMsg = err.Error()
		}

		logTool(toolName, req.Params.Arguments, success, resultSize, errorMsg)
		return result, err
	}
}

// WithSearchAnalytics wraps a search tool handler with search-specific analytics.
func WithSearchAnalytics(toolName string, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := ""
		if req.Params.Arguments != nil {
			if q, ok := req.Params.Arguments["query"].(string); ok {
				query = q
			}
		}

		result, err := handler(ctx, req)

		toolSuccess := err == nil && result != nil
		resultSize := 0
		errorMsg := ""
		resultCount := 0

		if result != nil {
			for _, content := range result.Content {
				switch c := content.(type) {
				case *mcp.TextContent:
					resultSize += len(c.Text)
					if c.Text != "No matches found." {
						resultCount = len(strings.Split(strings.TrimSpace(c.Text), "\n"))
					}
				case mcp.TextContent:
					resultSize += len(c.Text)
					if c.Text != "No matches found." {
						resultCount = len(strings.Split(strings.TrimSpace(c.Text), "\n"))
					}
				}
			}
		}

		if err != nil {
			errorMsg = err.Error()
		}

		logTool(toolName, req.Params.Arguments, toolSuccess, resultSize, errorMsg)
		logSearch(toolName, query, resultCount, toolSuccess && resultCount > 0)
		return result, err
	}
}

func logTool(toolName string, args map[string]interface{}, success bool, resultSize int, errorMsg string) {
	if globalAnalytics != nil {
		globalAnalytics.logToolInvocation(toolName, args, success, resultSize, errorMsg)
	}
}

func logSearch(toolName string, query string, resultCount int, success bool) {
	if globalAnalytics != nil {
		globalAnalytics.logSearchQuery(toolName, query, resultCount, success)
	}
}

func GetAnalyticsSummary() map[string]interface{} {
	if globalAnalytics != nil {
		return globalAnalytics.GetSummary()
	}
	return map[string]interface{}{}
}
