package mcp_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// The real capture_url tool runs behind the SSRF guard, so a loopback httptest
// URL is refused -- which is exactly the security property to assert. The happy
// path (fetch + parse) is covered by internal/capture's own tests.
func TestCaptureURL_RejectsUnsafeURL(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// file:// scheme is rejected before any dial. An explicit project=global keeps
	// the scope unambiguous so the call reaches the SSRF guard under test.
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "capture_url", Arguments: map[string]any{"url": "file:///etc/passwd", "project": "global"},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "scheme not allowed")

	// Missing url is a tool error too.
	res, err = cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "capture_url", Arguments: map[string]any{},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
}
