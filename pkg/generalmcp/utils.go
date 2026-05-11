package generalmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "general-mcp/server"
)

// getExeDir returns the directory containing the running executable,
// falling back to the working directory on error.
func getExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
		return "."
	}
	return filepath.Dir(exe)
}

func printStartupInfo(prompts []mcp.Prompt, tools []mcp.Tool) {
	var promptList []PromptInfo
	for _, p := range prompts {
		promptList = append(promptList, PromptInfo{Name: p.Name, Description: p.Description})
	}

	var toolList []ToolInfo
	for _, t := range tools {
		toolList = append(toolList, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema.Properties,
		})
	}

	info := StartupInfo{
		Type:    "general-mcp",
		Prompts: promptList,
		Tools:   toolList,
	}

	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		mcpserver.WriteDebugLog("Error marshaling startup info: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", string(b))
}
