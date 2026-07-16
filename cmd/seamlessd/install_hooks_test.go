package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeMCPAddArgs(t *testing.T) {
	args := claudeMCPAddArgs("http://127.0.0.1:8081", "k3y")
	require.Equal(t, []string{
		"mcp", "add", "--scope", "user", "--transport", "http", "seamless",
		"http://127.0.0.1:8081/api/mcp", "--header", "Authorization: Bearer k3y",
	}, args)
}
