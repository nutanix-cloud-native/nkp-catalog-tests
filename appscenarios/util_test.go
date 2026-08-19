package appscenarios

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAbsolutePathTo(t *testing.T) {
	catalogDir := t.TempDir()
	applicationDir := filepath.Join(catalogDir, "applications", "reloader", "v1")
	require.NoError(t, os.MkdirAll(applicationDir, 0o755))
	t.Setenv(catalogDirEnv, catalogDir)

	absAppPath, err := absolutePathTo("reloader")
	assert.NoError(t, err)

	expected := filepath.Join(catalogDir, "applications", "reloader", "v1")
	assert.Contains(t, absAppPath, expected)
}

func TestCatalogRootFallback(t *testing.T) {
	t.Setenv(catalogDirEnv, "")

	root, err := catalogRoot()
	require.NoError(t, err)

	expected, err := filepath.Abs(filepath.Join(".."))
	require.NoError(t, err)
	assert.Equal(t, expected, root)
}
