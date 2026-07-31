package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigPermissionsCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-bit cases")
	}

	write := func(t *testing.T, dirMode, fileMode os.FileMode) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "config")
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.Chmod(dir, dirMode))
		path := filepath.Join(dir, "seamless.yaml")
		require.NoError(t, os.WriteFile(path, []byte("mcp: {}\n"), 0o600))
		require.NoError(t, os.Chmod(path, fileMode))
		return path
	}

	t.Run("owner-only", func(t *testing.T) {
		got := configPermissionsCheck(write(t, 0o700, 0o600), "linux")
		require.Equal(t, statusOK, got.status)
	})

	t.Run("readable file", func(t *testing.T) {
		path := write(t, 0o700, 0o644)
		got := configPermissionsCheck(path, "linux")
		require.Equal(t, statusWarn, got.status)
		require.Contains(t, got.detail, "mode 0644")
		require.Contains(t, got.detail, "chmod 600 '"+path+"'")
	})

	t.Run("traversable directory", func(t *testing.T) {
		path := write(t, 0o755, 0o600)
		dir := filepath.Dir(path)
		got := configPermissionsCheck(path, "linux")
		require.Equal(t, statusWarn, got.status)
		require.Contains(t, got.detail, "mode 0755")
		require.Contains(t, got.detail, "chmod 700 '"+dir+"'")
	})

	t.Run("config symlink", func(t *testing.T) {
		realPath := write(t, 0o700, 0o600)
		linkPath := filepath.Join(filepath.Dir(realPath), "linked.yaml")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got := configPermissionsCheck(linkPath, "linux")
		require.Equal(t, statusWarn, got.status)
		require.Contains(t, got.detail, "is a symlink")
	})

	t.Run("containing directory symlink", func(t *testing.T) {
		realPath := write(t, 0o700, 0o600)
		linkDir := filepath.Join(t.TempDir(), "linked-config")
		if err := os.Symlink(filepath.Dir(realPath), linkDir); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		path := filepath.Join(linkDir, filepath.Base(realPath))
		got := configPermissionsCheck(path, "linux")
		require.Equal(t, statusWarn, got.status)
		require.Contains(t, got.detail, "containing directory")
		require.Contains(t, got.detail, "is a symlink")
	})
}

func TestConfigPermissionsCheck_QuotesRepairPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-bit case")
	}

	dir := filepath.Join(t.TempDir(), "owner's config")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "seamless config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("mcp: {}\n"), 0o644))

	got := configPermissionsCheck(path, "linux")
	require.Equal(t, statusWarn, got.status)
	require.Contains(t, got.detail, "chmod 600 '"+strings.ReplaceAll(path, "'", "'\"'\"'")+"'")
}

func TestConfigPermissionsCheck_NoFileAndWindows(t *testing.T) {
	got := configPermissionsCheck("", "linux")
	require.Equal(t, statusInfo, got.status)
	require.Contains(t, got.detail, "no config file")

	got = configPermissionsCheck(`C:\Users\owner\seamless.yaml`, "windows")
	require.Equal(t, statusInfo, got.status)
	require.Contains(t, got.detail, "Windows ACLs")
}
