package testutils

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteTestFile creates a temporary file with the given content and returns its path.
// The file is automatically cleaned up after the test completes.
func WriteTestFile(t *testing.T, content string) string {
	t.Helper()

	// create a temporary directory for the file
	tempDir, err := os.MkdirTemp("", "testutils-")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// register cleanup to remove the temporary directory and its contents
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Logf("failed to remove temporary directory %s: %v", tempDir, err)
		}
	})

	// create a file with a unique name
	tempFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(tempFile, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	return tempFile
}
