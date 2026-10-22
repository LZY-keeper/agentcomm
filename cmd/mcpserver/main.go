// Command mcpserver runs the MCP server over the stdio transport, exposing the
// built-in tool registry. Intended to run as a subprocess of an MCP client
// (the gateway's federation, or external clients like Claude Desktop). All
// logging is suppressed so stdout carries only JSON-RPC frames.
package main

import (
	"context"
	"os"

	"github.com/LZY-keeper/agentcomm/internal/mcp"
	"github.com/LZY-keeper/agentcomm/internal/tools"
)

func main() {
	reg := tools.Builtin()
	srv := mcp.NewServer("agentcomm-mcp", "1.0", reg)
	// ServeStdio reads os.Stdin, writes JSON-RPC to os.Stdout. Do NOT log here.
	if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
