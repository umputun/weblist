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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
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
func startUploadServer(t *testing.T, port int, extraArgs ...string) (root string, cleanup func()) {
	t.Helper()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	// use a temp copy of testdata so uploads don't pollute shared testdata
	tmpDir, err := os.MkdirTemp("", "weblist-e2e-upload-*")
	require.NoError(t, err)

	// copy testdata to temp dir
	copyDir(t, filepath.Join(cwd, testDataDir), tmpDir)

	args := make([]string, 0, 4+len(extraArgs))
	args = append(args,
		fmt.Sprintf("--listen=:%d", port),
		"--root="+tmpDir,
		"--upload.enabled",
		"--upload.max-size=1", // 1MB max for tests
	)
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
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
	text, err := toast.TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 1 file", text)
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
	waitVisible(t, page.Locator("#upload-toast:has-text('already exists')"))
	text, err := toast.TextContent()
	require.NoError(t, err)
	assert.Equal(t, `Uploaded 0 files, 1 failed: sample.txt (file "sample.txt" already exists)`, text)
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

func pickFiles(t *testing.T, page playwright.Page, paths []string) {
	t.Helper()
	fc, err := page.ExpectFileChooser(func() error {
		return page.Locator("#upload-btn").Click()
	})
	require.NoError(t, err)
	require.NoError(t, fc.SetFiles(paths))
}

func writeTempFiles(t *testing.T, names []string, content string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 0, len(names))
	for _, name := range names {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		paths = append(paths, p)
	}
	return paths
}

func holdUploads(t *testing.T, page playwright.Page, release <-chan struct{}) *atomic.Int32 {
	t.Helper()
	var intercepted atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		intercepted.Add(1)
		<-release
		_ = route.Continue()
	}))
	return &intercepted
}

func waitCount(t *testing.T, counter *atomic.Int32, want int32) {
	t.Helper()
	require.Eventually(t, func() bool { return counter.Load() >= want }, 10*time.Second, 50*time.Millisecond)
}

func TestUpload_FilenamesMatchingObjectPropertiesAreUploaded(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	names := []string{"constructor", "toString", "__proto__", "ordinary.txt"}
	pickFiles(t, page, writeTempFiles(t, names, "proto"))

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 4 files')"))
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)
		assert.Equal(t, "proto", string(content), name)
	}
}

func TestUpload_NetworkErrorNotRetried(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var attempts atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		attempts.Add(1)
		_ = route.Abort()
	}))

	pickFiles(t, page, writeTempFiles(t, []string{"net.txt"}, "x"))

	waitVisible(t, page.Locator("#upload-toast:has-text('network error')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 0 files, 1 failed: net.txt (network error)", text)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(1), attempts.Load())
	_, err = os.Stat(filepath.Join(root, "net.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestUpload_DuplicateAgainstQueuedEntrySkipped(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	release := make(chan struct{})
	intercepted := holdUploads(t, page, release)

	names := []string{"dup-01.txt", "dup-02.txt", "dup-03.txt", "dup-04.txt", "dup-05.txt", "dup-06.txt"}
	pickFiles(t, page, writeTempFiles(t, names, "x"))
	waitCount(t, intercepted, 4)

	pickFiles(t, page, writeTempFiles(t, []string{"dup-05.txt"}, "y"))
	waitVisible(t, page.Locator("#upload-toast:has-text('1 skipped as duplicates')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 0 files, 1 skipped as duplicates", text)

	close(release)
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 6 files')"))
	assert.Equal(t, int32(6), intercepted.Load())
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)
		assert.Equal(t, "x", string(content), name)
	}
}

func TestUpload_OverlappingSelectionsKeepTheirTargets(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	release := make(chan struct{})
	intercepted := holdUploads(t, page, release)

	pickFiles(t, page, writeTempFiles(t, []string{"root-a.txt"}, "root"))
	waitCount(t, intercepted, 1)

	require.NoError(t, page.Locator("tr.dir-row:has-text('subdir')").Click())
	require.NoError(t, page.WaitForURL("**/*path=subdir*"))
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))

	pickFiles(t, page, writeTempFiles(t, []string{"sub-b.txt"}, "sub"))
	waitCount(t, intercepted, 2)

	close(release)
	require.Eventually(t, func() bool {
		_, errA := os.Stat(filepath.Join(root, "root-a.txt"))
		_, errB := os.Stat(filepath.Join(root, "subdir", "sub-b.txt"))
		return errA == nil && errB == nil
	}, 10*time.Second, 50*time.Millisecond)
	_, err = os.Stat(filepath.Join(root, "subdir", "root-a.txt"))
	assert.True(t, os.IsNotExist(err), "root selection must not follow the navigation")
	_, err = os.Stat(filepath.Join(root, "sub-b.txt"))
	assert.True(t, os.IsNotExist(err), "subdir selection must not land in the root")
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
}

func TestUpload_SummaryToastListsFailures(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	paths := writeTempFiles(t, []string{"ok1.txt", "ok2.txt"}, "ok")
	big := filepath.Join(t.TempDir(), "big.bin")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("b", 1<<20+1)), 0o644))
	pickFiles(t, page, []string{paths[0], big, paths[1]})

	waitVisible(t, page.Locator("#upload-toast:has-text('1 failed')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 2 files, 1 failed: big.bin (exceeds maximum size)", text)
	for _, name := range []string{"ok1.txt", "ok2.txt"} {
		_, err := os.Stat(filepath.Join(root, name))
		assert.NoError(t, err, name)
	}
	_, err = os.Stat(filepath.Join(root, "big.bin"))
	assert.True(t, os.IsNotExist(err))
}

func TestUpload_HtmlLoginResponseReportedAsFailure(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		_ = route.Fulfill(playwright.RouteFulfillOptions{
			Status:      new(200),
			ContentType: new("text/html"),
			Body:        "<html><body>login</body></html>",
		})
	}))

	pickFiles(t, page, writeTempFiles(t, []string{"login.txt"}, "x"))

	waitVisible(t, page.Locator("#upload-toast:has-text('unexpected response')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 0 files, 1 failed: login.txt (unexpected response)", text)
	_, err = os.Stat(filepath.Join(root, "login.txt"))
	assert.True(t, os.IsNotExist(err))
}

// createMultipartUpload builds a multipart form body with the given path and files
func createMultipartUpload(t *testing.T, path string, files map[string]string) (body *bytes.Buffer, contentType string) {
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
