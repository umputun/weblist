package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootFSContainsResolvedSymlinks(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "root")
	rootAlias := filepath.Join(base, "root-alias")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(realRoot, "inside"), 0o750))
	require.NoError(t, os.MkdirAll(outside, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(realRoot, "plain.txt"), []byte("plain"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(realRoot, "inside", "media.txt"), []byte("inside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(realRoot, rootAlias))
	require.NoError(t, os.Symlink(filepath.Join(realRoot, "inside"), filepath.Join(realRoot, "absolute-inside")))
	require.NoError(t, os.Symlink("inside", filepath.Join(realRoot, "relative-inside")))
	require.NoError(t, os.Symlink(outside, filepath.Join(realRoot, "escape")))

	rootFS, err := newRootFS(rootAlias)
	require.NoError(t, err)
	require.Implements(t, (*fs.StatFS)(nil), rootFS)
	require.Implements(t, (*fs.ReadDirFS)(nil), rootFS)

	openTests := []struct {
		name string
		want string
	}{
		{name: "plain.txt", want: "plain"},
		{name: "absolute-inside/media.txt", want: "inside"},
		{name: "relative-inside/media.txt", want: "inside"},
	}
	for _, tt := range openTests {
		t.Run("opens "+tt.name, func(t *testing.T) {
			got, readErr := fs.ReadFile(rootFS, tt.name)
			require.NoError(t, readErr)
			assert.Equal(t, tt.want, string(got))
		})
	}

	for _, name := range []string{"absolute-inside", "relative-inside"} {
		t.Run("stats and lists "+name, func(t *testing.T) {
			info, statErr := fs.Stat(rootFS, name)
			require.NoError(t, statErr)
			assert.True(t, info.IsDir())

			entries, readDirErr := fs.ReadDir(rootFS, name)
			require.NoError(t, readDirErr)
			require.Len(t, entries, 1)
			assert.Equal(t, "media.txt", entries[0].Name())
		})
	}

	t.Run("rejects an escaping symlink for every filesystem operation", func(t *testing.T) {
		_, openErr := rootFS.Open("escape/secret.txt")
		assert.True(t, errors.Is(openErr, fs.ErrNotExist), openErr)

		_, statErr := fs.Stat(rootFS, "escape/secret.txt")
		assert.True(t, errors.Is(statErr, fs.ErrNotExist), statErr)

		_, readDirErr := fs.ReadDir(rootFS, "escape")
		assert.True(t, errors.Is(readDirErr, fs.ErrNotExist), readDirErr)
	})
}

func TestNewRootFSRejectsMissingRoot(t *testing.T) {
	_, err := newRootFS(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
}
