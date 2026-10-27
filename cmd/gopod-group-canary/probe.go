package main

import (
	"github.com/Coriol-is/gopod/internal/runner"
	"strings"
)

// Inspect only in memory. Neither provider output nor error strings leave this
// trusted diagnostic; values below are fixed structural booleans.
func probeReport(provider string, res *runner.ExecResult) map[string]any {
	text := strings.ToLower(res.Stdout + "\n" + res.Stderr)
	return map[string]any{
		"provider": provider, "inference_qualified": false, "exit": res.ExitCode,
		"marker":           strings.Contains(res.Stdout, "GROUP_CANARY_OK"),
		"config_error":     strings.Contains(text, "config") && strings.Contains(text, "error"),
		"mcp_error":        strings.Contains(text, "mcp") && (strings.Contains(text, "failed") || strings.Contains(text, "error")),
		"relay_denied":     strings.Contains(text, "egress unavailable"),
		"forbidden":        strings.Contains(text, "403") || strings.Contains(text, "forbidden"),
		"unauthorized":     strings.Contains(text, "401") || strings.Contains(text, "unauthorized"),
		"tls_error":        strings.Contains(text, "certificate") || strings.Contains(text, "tls"),
		"connection_error": strings.Contains(text, "connection") || strings.Contains(text, "connect error"),
		"rate_limit":       strings.Contains(text, "429") || strings.Contains(text, "quota") || strings.Contains(text, "usage limit"),
	}
}
