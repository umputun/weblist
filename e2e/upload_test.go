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
	uploadBaseURL    = "http://localhost:18082"
	uploadAuthURL    = "http://localhost:18083"
	uploadNoAuthURL  = "http://localhost:18084" // upload disabled server for visibility test
	uploadExcludeURL = "http://localhost:18085"
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

func TestUpload_ManyFilesComplete(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	names := make([]string, 0, 60)
	for i := range 60 {
		names = append(names, fmt.Sprintf("many-%02d.txt", i))
	}
	pickFiles(t, page, writeTempFiles(t, names, "x"))

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 60 files')"))
	for _, name := range names {
		_, err := os.Stat(filepath.Join(root, name))
		assert.NoError(t, err, name)
	}
	waitVisible(t, page.Locator("td:has-text('many-59.txt')"))
}

func TestUpload_RetriesOn429(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var attempts atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		if attempts.Add(1) <= 2 {
			_ = route.Fulfill(playwright.RouteFulfillOptions{Status: new(429), ContentType: new("text/plain"), Body: "Too Many Requests"})
			return
		}
		_ = route.Continue()
	}))

	pickFiles(t, page, writeTempFiles(t, []string{"retry.txt"}, "again"))

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
	assert.Equal(t, int32(3), attempts.Load())
	content, err := os.ReadFile(filepath.Join(root, "retry.txt"))
	require.NoError(t, err)
	assert.Equal(t, "again", string(content))
}

func TestUpload_StopsAfterRetryLimit(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var attempts atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		attempts.Add(1)
		_ = route.Fulfill(playwright.RouteFulfillOptions{Status: new(429), ContentType: new("text/plain"), Body: "Too Many Requests"})
	}))

	pickFiles(t, page, writeTempFiles(t, []string{"limited.txt"}, "x"))

	waitVisible(t, page.Locator("#upload-toast:has-text('Too Many Requests')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 0 files, 1 failed: limited.txt (Too Many Requests)", text)
	assert.Equal(t, int32(6), attempts.Load())
	_, err = os.Stat(filepath.Join(root, "limited.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestUpload_NavigationDuringUploadKeepsNewDirectory(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	release := make(chan struct{})
	intercepted := holdUploads(t, page, release)

	pickFiles(t, page, writeTempFiles(t, []string{"held.txt"}, "x"))
	waitCount(t, intercepted, 1)

	require.NoError(t, page.Locator("tr.dir-row:has-text('subdir')").Click())
	require.NoError(t, page.WaitForURL("**/*path=subdir*"))
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))

	close(release)
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
	time.Sleep(500 * time.Millisecond)

	url := page.URL()
	assert.Contains(t, url, "path=subdir")
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))
	count, err := page.Locator("td:has-text('held.txt')").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "root listing must not replace the subdirectory")
	_, err = os.Stat(filepath.Join(root, "held.txt"))
	assert.NoError(t, err)
}

func TestUpload_LateRefreshDoesNotReplaceNavigation(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	release := make(chan struct{})
	var heldRefresh atomic.Int32
	require.NoError(t, page.Route("**/partials/dir-contents*", func(route playwright.Route) {
		if v, _ := route.Request().HeaderValue("X-Upload-Refresh"); v == "" {
			_ = route.Continue()
			return
		}
		heldRefresh.Add(1)
		<-release
		_ = route.Continue()
	}))

	pickFiles(t, page, writeTempFiles(t, []string{"late.txt"}, "x"))
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
	waitCount(t, &heldRefresh, 1)

	_, err = page.Evaluate(`() => {
		window.__refreshLoadEnd = false;
		document.addEventListener('htmx:beforeSwap', function (evt) {
			var cfg = evt.detail && evt.detail.requestConfig;
			if (!cfg || !cfg.headers || !('X-Upload-Refresh' in cfg.headers)) return;
			evt.detail.xhr.addEventListener('loadend', function () { window.__refreshLoadEnd = true; });
		});
	}`)
	require.NoError(t, err)

	require.NoError(t, page.Locator("tr.dir-row:has-text('subdir')").Click())
	require.NoError(t, page.WaitForURL("**/*path=subdir*"))
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))

	close(release)
	_, err = page.WaitForFunction("() => window.__refreshLoadEnd === true", nil)
	require.NoError(t, err)
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))
	count, err := page.Locator("td:has-text('late.txt')").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "late refresh must not replace the subdirectory listing")
}

func TestUpload_ControlsWorkAfterHistoryBack(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	require.NoError(t, page.Locator("tr.dir-row:has-text('subdir')").Click())
	require.NoError(t, page.WaitForURL("**/*path=subdir*"))
	waitVisible(t, page.Locator("td:has-text('nested.txt')"))

	_, err = page.GoBack()
	require.NoError(t, err)
	waitVisible(t, page.Locator("td:has-text('sample.txt')"))

	pickFiles(t, page, writeTempFiles(t, []string{"after-back.txt"}, "x"))
	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 1 file')"))
	content, err := os.ReadFile(filepath.Join(root, "after-back.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(content))
	waitVisible(t, page.Locator("td:has-text('after-back.txt')"))
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tree")
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return dir
}

const fakeEntryHelpers = `
	window.__readEntriesCalls = 0;
	function fileEntry(name, content, fail) {
		return { isFile: true, isDirectory: false, name: name, file: function (ok, err) {
			if (fail) { err({ name: 'NotReadableError' }); return; }
			ok(new File([content], name));
		} };
	}
	function dirEntry(name, batches, fail) {
		return { isFile: false, isDirectory: true, name: name, createReader: function () {
			var i = 0;
			return { readEntries: function (ok, err) {
				window.__readEntriesCalls++;
				if (fail) { err({ name: 'NotFoundError' }); return; }
				ok(i < batches.length ? batches[i++] : []);
			} };
		} };
	}
	function fakeTree(failAll) {
		return [dirEntry('fake', [
			[fileEntry('f1.txt', 'one', failAll), fileEntry('f2.txt', 'two', failAll)],
			[fileEntry('f3.txt', 'three', true), dirEntry('bad', [], true), dirEntry('deep', [[fileEntry('f4.txt', 'four', failAll)]], false)]
		], false)];
	}
	function manyBatches(n) {
		var batches = [];
		for (var b = 0; b < n; b++) batches.push([fileEntry('a' + b + '.txt', 'x', false), fileEntry('b' + b + '.txt', 'x', false)]);
		return [dirEntry('big', batches, false)];
	}
`

func TestUpload_FolderButtonVisibleWhenEnabled(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	visible, err := page.Locator("#upload-folder-btn").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible)
}

func TestUpload_FolderButtonHiddenWhenDisabled(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	count, err := page.Locator("#upload-folder-btn").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestUpload_FolderPickerCreatesTree(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	tree := writeTree(t, map[string]string{"a.txt": "A", "sub/b.txt": "B", "sub/deep/c.txt": "C"})
	require.NoError(t, page.Locator("#upload-folder-input").SetInputFiles(tree))

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 3 files')"))
	for rel, want := range map[string]string{"tree/a.txt": "A", "tree/sub/b.txt": "B", "tree/sub/deep/c.txt": "C"} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		assert.Equal(t, want, string(content), rel)
	}
	waitVisible(t, page.Locator("td:has-text('tree')"))
}

func TestUpload_FolderPickerHonorsExcludeOnFilename(t *testing.T) {
	root, cleanup := startUploadServer(t, 18085, "--exclude=.env")
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadExcludeURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	tree := writeTree(t, map[string]string{"ok.txt": "ok", ".env": "SECRET=1"})
	require.NoError(t, page.Locator("#upload-folder-input").SetInputFiles(tree))

	waitVisible(t, page.Locator("#upload-toast:has-text('1 failed')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, `Uploaded 1 file, 1 failed: tree/.env (access denied to ".env")`, text)
	_, err = os.Stat(filepath.Join(root, "tree", "ok.txt"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "tree", ".env"))
	assert.True(t, os.IsNotExist(err))
}

func TestUpload_FolderPickerRefusesOverMaxFiles(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var requests atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		requests.Add(1)
		_ = route.Continue()
	}))

	files := make(map[string]string, 1001)
	for i := range 1001 {
		files[fmt.Sprintf("f%04d.txt", i)] = "x"
	}
	require.NoError(t, page.Locator("#upload-folder-input").SetInputFiles(writeTree(t, files)))

	waitVisible(t, page.Locator("#upload-toast:has-text('limit is 1000')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "More than 1000 files; the limit is 1000", text)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(0), requests.Load())
}

func TestUpload_WalkEntriesAlgorithm(t *testing.T) {
	root, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	truncated, err := page.Evaluate(`async () => {` + fakeEntryHelpers + `
		const res = await window.weblistUpload.walkEntries(manyBatches(8), 2);
		return 'truncated=' + res.truncated + ' collected=' + res.entries.length + ' reads=' + window.__readEntriesCalls;
	}`)
	require.NoError(t, err)
	assert.Equal(t, "truncated=true collected=2 reads=2", truncated, "paging must stop once the budget is exceeded")

	full, err := page.Evaluate(`async () => {` + fakeEntryHelpers + `
		const res = await window.weblistUpload.walkEntries(fakeTree(false), 1000);
		const sel = { targetDir: '.', total: 0, done: 0, failed: [], skipped: [], refreshPending: false };
		window.weblistUpload.selections.push(sel);
		window.weblistUpload.enqueue(sel, res.entries, res.errors, res.truncated);
		return {
			truncated: res.truncated,
			paths: res.entries.map(e => e.relativeDir + '/' + e.file.name),
			errors: res.errors.map(e => e.relativePath + ' ' + e.reason)
		};
	}`)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"truncated": false,
		"paths":     []any{"fake/f1.txt", "fake/f2.txt", "fake/deep/f4.txt"},
		"errors":    []any{"fake/f3.txt unreadable: NotReadableError", "fake/bad unreadable directory: NotFoundError"},
	}, full)

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 3 files')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	want := "Uploaded 3 files, 2 failed: fake/f3.txt (unreadable: NotReadableError), fake/bad (unreadable directory: NotFoundError)"
	assert.Equal(t, want, text)
	for rel, want := range map[string]string{"fake/f1.txt": "one", "fake/f2.txt": "two", "fake/deep/f4.txt": "four"} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		assert.Equal(t, want, string(content), rel)
	}
}

func TestUpload_AllUnreadableSelectionSettles(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var requests atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		requests.Add(1)
		_ = route.Continue()
	}))

	_, err = page.Evaluate(`async () => {` + fakeEntryHelpers + `
		const res = await window.weblistUpload.walkEntries(fakeTree(true), 1000);
		const sel = { targetDir: '.', total: 0, done: 0, failed: [], skipped: [], refreshPending: false };
		window.weblistUpload.selections.push(sel);
		window.weblistUpload.enqueue(sel, res.entries, res.errors, res.truncated);
	}`)
	require.NoError(t, err)

	waitVisible(t, page.Locator("#upload-toast:has-text('Uploaded 0 files, 5 failed')"))
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(0), requests.Load())
}

func TestUpload_DropWithUnreadableItemsReported(t *testing.T) {
	_, cleanup := startUploadServer(t, 18082)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(uploadBaseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))

	var requests atomic.Int32
	require.NoError(t, page.Route("**/upload", func(route playwright.Route) {
		requests.Add(1)
		_ = route.Continue()
	}))

	_, err = page.Evaluate(`() => {
		const dt = new DataTransfer();
		dt.items.add(new File(['x'], 'synthetic.txt'));
		dt.items.add(new File(['y'], 'other.txt'));
		document.getElementById('file-listing').dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
	}`)
	require.NoError(t, err)

	waitVisible(t, page.Locator("#upload-toast:has-text('2 failed')"))
	text, err := page.Locator("#upload-toast").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "Uploaded 0 files, 2 failed: item 1 (unreadable), item 2 (unreadable)", text)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(0), requests.Load())
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
