// Command opnsense-mcp is a stdio MCP server for the OPNsense HTTP API.
// Configure with OPNSENSE_URL, OPNSENSE_API_KEY, OPNSENSE_API_SECRET and
// optionally OPNSENSE_VERIFY_TLS (default false) and OPNSENSE_TIMEOUT_MS.
package main

import (
	"fmt"
	"os"

	"github.com/SystemFiles/grokbot-opnsense-plugin/internal/mcp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Fprintln(os.Stderr, "opnsense-mcp", mcp.Version)
		return
	}
	if err := mcp.Serve(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "opnsense-mcp:", err)
		os.Exit(1)
	}
}
