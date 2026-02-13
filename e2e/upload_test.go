//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upload tests run on separate server instances to avoid conflicts with main server
const (
	uploadBaseURL   = "http://localhost:18082"
	uploadAuthURL   = "http://localhost:18083"
	uploadNoAuthURL = "http://localhost:18084" // upload disabled server for visibility test
)

// startUploadServer starts a server with upload enabled
func startUploadServer(t *testing.T, port int, extraArgs ...string) (string, func()) {
	t.Helper()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	// use a temp copy of testdata so uploads don't pollute shared testdata
	tmpDir, err := os.MkdirTemp("", "weblist-e2e-upload-*")
	require.NoError(t, err)

	// copy testdata to temp dir
	copyDir(t, filepath.Join(cwd, testDataDir), tmpDir)

	args := []string{
		fmt.Sprintf("--listen=:%d", port),
		"--root=" + tmpDir,
		"--upload.enabled",
		"--upload.max-size=1", // 1MB max for tests
	}
	args = append(args, extraArgs...)

	cmd := exec.Command("/tmp/weblist-e2e", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to start upload server: %v", err)
	}

	serverURL := fmt.Sprintf("http://localhost:%d", port)
	if err := waitForServer(serverURL+"/ping", 10*time.Second); err != nil {
		_ = cmd.Process.Kill()
		os.RemoveAll(tmpDir)
		t.Fatalf("upload server not ready: %v", err)
	}

	return tmpDir, func() {
		_ = cmd.Process.Kill()
		os.RemoveAll(tmpDir)
	}
}

// copyDir recursively copies src directory contents to dst
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			require.NoError(t, os.MkdirAll(dstPath, 0o750))
			copyDir(t, srcPath, dstPath)
		} else {
			data, err := os.ReadFile(srcPath)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(dstPath, data, 0o644))
		}
	}
}

// --- upload button visibility tests ---

func TestUpload_ButtonVisibleWhenEnabled(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)

	waitVisible(t, page.Locator("table"))

	// upload button should be visible
	visible, err := page.Locator("#upload-btn").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "upload button should be visible when upload is enabled")
}

func TestUpload_ButtonHiddenWhenDisabled(t *testing.T) {
	// main server on port 18080 does not have upload enabled
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	waitVisible(t, page.Locator("table"))

	// upload button should not be present
	count, err := page.Locator("#upload-btn").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "upload button should not exist when upload is disabled")
}

// --- toast notification tests ---

func TestUpload_ToastOnSuccessfulUpload(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	// set up file chooser handler before clicking upload
	fc, err := page.ExpectFileChooser(func() error {
		return page.Locator("#upload-btn").Click()
	})
	require.NoError(t, err)

	// create a temp file to upload
	tmpFile := filepath.Join(t.TempDir(), "toast-success.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("toast test"), 0o644))
	require.NoError(t, fc.SetFiles(tmpFile))

	// toast should appear with success message
	toast := page.Locator("#upload-toast")
	waitVisible(t, toast)
	text, err := toast.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "Uploaded:")
	assert.Contains(t, text, "toast-success.txt")
}

func TestUpload_ToastOnDuplicateFile(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	// upload sample.txt which already exists in testdata (no overwrite)
	fc, err := page.ExpectFileChooser(func() error {
		return page.Locator("#upload-btn").Click()
	})
	require.NoError(t, err)

	// create a file named sample.txt (same name as existing testdata file)
	tmpFile := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("duplicate"), 0o644))
	require.NoError(t, fc.SetFiles(tmpFile))

	// toast should appear with error message
	toast := page.Locator("#upload-toast")
	waitVisible(t, toast)
	text, err := toast.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "already exists")
}

// --- file upload via API tests ---

func TestUpload_SingleFile(t *testing.T) {
	tmpDir, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	// upload a file via multipart POST
	body, contentType := createMultipartUpload(t, ".", map[string]string{
		"uploaded.txt": "hello from e2e test",
	})

	resp, err := http.Post(uploadBaseURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(respBody), "uploaded.txt")

	// verify file was written to disk
	content, err := os.ReadFile(filepath.Join(tmpDir, "uploaded.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello from e2e test", string(content))
}

func TestUpload_FileAppearsInListing(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)

	waitVisible(t, page.Locator("table"))

	// upload a file via API
	body, contentType := createMultipartUpload(t, ".", map[string]string{
		"e2e-test-file.txt": "test content",
	})
	resp, err := http.Post(uploadBaseURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// reload the page and verify file appears in listing
	_, err = page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	visible, err := page.Locator("text=e2e-test-file.txt").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "uploaded file should appear in file listing")
}

func TestUpload_DuplicateFileRejected(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	// sample.txt already exists in testdata, should be 409 conflict
	body, contentType := createMultipartUpload(t, ".", map[string]string{
		"sample.txt": "new content",
	})
	resp, err := http.Post(uploadBaseURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(respBody), "already exists")
}

func TestUpload_WithAuthEnabled(t *testing.T) {
	_, cleanup := startUploadServer(t, 18083, "--auth=testpass123", "--insecure-cookies")
	defer cleanup()

	// upload without auth should fail (redirect to login)
	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	body, contentType := createMultipartUpload(t, ".", map[string]string{
		"auth-test.txt": "auth test content",
	})
	resp, err := noRedirectClient.Post(uploadAuthURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "upload without auth should redirect to login")

	// login via browser to get auth cookie, then upload via API
	page := newPage(t)
	_, err = page.Goto(uploadAuthURL + "/login")
	require.NoError(t, err)
	waitVisible(t, page.Locator("input[name='password']"))
	require.NoError(t, page.Locator("input[name='password']").Fill("testpass123"))
	require.NoError(t, page.Locator("button[type='submit']").Click())
	require.NoError(t, page.WaitForURL(uploadAuthURL+"/"))

	// verify upload button is visible after login
	waitVisible(t, page.Locator("table"))
	visible, err := page.Locator("#upload-btn").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "upload button should be visible after authentication")
}

// --- upload error handling tests ---

func TestUpload_PathTraversalBlocked(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	body, contentType := createMultipartUpload(t, "../../etc", map[string]string{
		"evil.txt": "malicious content",
	})
	resp, err := http.Post(uploadBaseURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(respBody), "path traversal")
}

func TestUpload_DisabledServerRejectsUpload(t *testing.T) {
	// main server on port 18080 does not have upload enabled
	body, contentType := createMultipartUpload(t, ".", map[string]string{
		"test.txt": "should fail",
	})
	resp, err := http.Post(baseURL+"/upload", contentType, body) //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	// should be 404 (route not registered) or 405 method not allowed
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "upload should not succeed on disabled server")
}

// createMultipartUpload builds a multipart form body with the given path and files
func createMultipartUpload(t *testing.T, path string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	require.NoError(t, writer.WriteField("path", path))

	for name, content := range files {
		part, err := writer.CreateFormFile("file", name)
		require.NoError(t, err)
		_, err = part.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())
	return &buf, writer.FormDataContentType()
}
