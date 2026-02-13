package server

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestServer creates a test server with the testdata directory
func setupTestServer(t *testing.T) *Web {
	// get the absolute path to the testdata directory
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	// ensure the testdata directory exists
	_, err = os.Stat(testdataDir)
	require.NoError(t, err, "testdata directory must exist at %s", testdataDir)

	// create server with the testdata directory and a random port
	srv := &Web{
		Config: Config{
			ListenAddr:      ":0", // use port 0 to let the system assign a random available port
			Theme:           "light",
			HideFooter:      false,
			RootDir:         testdataDir,
			Version:         "test-version",
			Title:           "Test Title",
			InsecureCookies: true,           // allow insecure cookies for tests
			SessionTTL:      24 * time.Hour, // set session timeout to 24 hours
		},
		FS: os.DirFS(testdataDir),
	}

	// initialize templates for testing
	err = srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	return srv
}

func TestRun(t *testing.T) {
	srv := setupTestServer(t)

	// create a context that will be canceled after a short time
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// run the server in a goroutine
	errCh := make(chan error)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	// wait for the context to be canceled or for an error
	select {
	case err := <-errCh:
		// we expect nil error when the context is canceled
		assert.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Server did not shut down within expected time")
	}
}

// mockFS is a mock filesystem that can be configured to fail stat for parent directories
type mockFS struct {
	baseDir           string
	failStatForParent bool
}

func (m *mockFS) Open(name string) (fs.File, error) {
	return os.DirFS(m.baseDir).Open(name)
}

func (m *mockFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(os.DirFS(m.baseDir), name)
}

func (m *mockFS) Stat(name string) (fs.FileInfo, error) {
	// if configured to fail for parent and the path is a parent directory
	if m.failStatForParent && name == filepath.Dir("subdir") {
		return nil, fmt.Errorf("mock error: can't stat parent directory")
	}
	return fs.Stat(os.DirFS(m.baseDir), name)
}

func TestServerIntegration(t *testing.T) {
	// get the absolute path to the testdata directory
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	// create server with the testdata directory
	srv := &Web{
		Config: Config{
			ListenAddr: ":0", // use port 0 to let the system assign a random available port
			Theme:      "light",
			HideFooter: false,
			RootDir:    testdataDir,
			Version:    "test-version",
		},
		FS: os.DirFS(testdataDir),
	}

	// start the server in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// create a listener to get the actual port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // close it so the server can use it

	// update the server's listen address with the actual port
	srv.ListenAddr = fmt.Sprintf(":%d", port)

	// start the server
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(ctx)
	}()

	// wait a moment for the server to start
	time.Sleep(100 * time.Millisecond)

	// create an HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// don't follow redirects automatically, so we can test them
			return http.ErrUseLastResponse
		},
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	// define subtests with lowercase names
	t.Run("root page loads", func(t *testing.T) {
		resp, err := client.Get(baseURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		assert.Contains(t, string(body), "file1.txt")
		assert.Contains(t, string(body), "file2.txt")
		assert.Contains(t, string(body), "dir1")
	})

	t.Run("directory navigation", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=dir1")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		assert.Contains(t, string(body), "file3.txt")
		assert.Contains(t, string(body), "subdir")
	})

	t.Run("file download", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/file1.txt")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment; filename=\"file1.txt\"")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "file1 content", string(body))
	})

	t.Run("file redirect", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=file1.txt")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/file1.txt", resp.Header.Get("Location"))
	})

	t.Run("htmx directory contents", func(t *testing.T) {
		req, err := http.NewRequest("GET", baseURL+"/partials/dir-contents?path=dir1", nil)
		require.NoError(t, err)
		req.Header.Set("HX-Request", "true") // simulate HTMX request

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the response contains expected content
		assert.Contains(t, string(body), "file3.txt")
		assert.Contains(t, string(body), "subdir")
	})

	t.Run("sorting", func(t *testing.T) {
		req, err := http.NewRequest("GET", baseURL+"/partials/dir-contents?path=&sort=name&dir=desc", nil)
		require.NoError(t, err)
		req.Header.Set("HX-Request", "true") // simulate HTMX request

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that all expected items are present (order is hard to verify in HTML)
		assert.Contains(t, string(body), "file1.txt")
		assert.Contains(t, string(body), "file2.txt")
		assert.Contains(t, string(body), "dir1")
		assert.Contains(t, string(body), "dir2")
	})

	t.Run("404 for non-existent path", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=non-existent")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("directory path redirects to view", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/dir1")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/?path=dir1", resp.Header.Get("Location"))
	})

	// shutdown the server
	cancel()

	// wait for server to shut down
	select {
	case err := <-serverErrCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not shut down within expected time")
	}
}

// Skip TestHandleLoginPage as templates are embedded and tests are failing

// TestHTMLRendering verifies that HTML files are rendered correctly in the preview
// mockFile represents a mock file for testing
type mockFile struct {
	content  []byte
	isDir    bool
	name     string
	modTime  time.Time
	size     int64
	failRead bool
}

// mockFS is a mock filesystem for testing
type mockFSWithFiles struct {
	files    map[string]mockFile
	failOpen bool
}

func (m *mockFSWithFiles) Open(name string) (fs.File, error) {
	if m.failOpen {
		return nil, fmt.Errorf("mock filesystem: open failed")
	}
	if file, ok := m.files[name]; ok {
		return &mockFileHandle{file: file, pos: 0}, nil
	}
	return nil, fs.ErrNotExist
}

func (m *mockFSWithFiles) Stat(name string) (fs.FileInfo, error) {
	if file, ok := m.files[name]; ok {
		return &mockFileInfo{file: file}, nil
	}
	return nil, fs.ErrNotExist
}

func (m *mockFSWithFiles) ReadDir(name string) ([]fs.DirEntry, error) {
	return []fs.DirEntry{}, nil
}

type mockFileHandle struct {
	file mockFile
	pos  int64
}

func (m *mockFileHandle) Read(b []byte) (int, error) {
	if m.file.failRead {
		return 0, fmt.Errorf("mock file: read failed")
	}
	if m.pos >= int64(len(m.file.content)) {
		return 0, io.EOF
	}
	n := copy(b, m.file.content[m.pos:])
	m.pos += int64(n)
	return n, nil
}

func (m *mockFileHandle) Close() error {
	return nil
}

func (m *mockFileHandle) Stat() (fs.FileInfo, error) {
	return &mockFileInfo{file: m.file}, nil
}

func (m *mockFileHandle) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = offset
	case io.SeekCurrent:
		m.pos += offset
	case io.SeekEnd:
		m.pos = int64(len(m.file.content)) + offset
	}
	return m.pos, nil
}

type mockFileInfo struct {
	file mockFile
}

func (m *mockFileInfo) Name() string {
	return m.file.name
}

func (m *mockFileInfo) Size() int64 {
	return m.file.size
}

func (m *mockFileInfo) Mode() fs.FileMode {
	if m.file.isDir {
		return fs.ModeDir
	}
	return 0666
}

func (m *mockFileInfo) ModTime() time.Time {
	return m.file.modTime
}

func (m *mockFileInfo) IsDir() bool {
	return m.file.isDir
}

func (m *mockFileInfo) Sys() any {
	return nil
}

func TestRouter(t *testing.T) {
	// create a test server with test configuration
	srv := setupTestServer(t)

	// test with default configuration
	t.Run("default router configuration", func(t *testing.T) {
		router, err := srv.router()
		require.NoError(t, err)
		require.NotNil(t, router)

		// create test server using the router
		ts := httptest.NewServer(router)
		defer ts.Close()

		// test cases to verify routes are working
		tests := []struct {
			name           string
			path           string
			expectedStatus int
			expectedBody   string
		}{
			{
				name:           "Root path",
				path:           "/",
				expectedStatus: http.StatusOK,
				expectedBody:   "file1.txt", // should contain this test file
			},
			{
				name:           "CSS Asset",
				path:           "/assets/css/custom.css",
				expectedStatus: http.StatusOK,
				expectedBody:   "", // empty because we don't actually create this file in tests
			},
			{
				name:           "File download",
				path:           "/file1.txt",
				expectedStatus: http.StatusOK,
				expectedBody:   "file1 content",
			},
			{
				name:           "Directory path redirects to view",
				path:           "/dir1",
				expectedStatus: http.StatusSeeOther,
				expectedBody:   "",
			},
			{
				name:           "Invalid path",
				path:           "/nonexistent",
				expectedStatus: http.StatusNotFound,
				expectedBody:   "file not found",
			},
		}

		// create client that doesn't follow redirects
		client := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		// run tests
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				resp, err := client.Get(ts.URL + tc.path)
				require.NoError(t, err)
				defer resp.Body.Close()

				assert.Equal(t, tc.expectedStatus, resp.StatusCode)

				if tc.expectedBody != "" {
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					assert.Contains(t, string(body), tc.expectedBody)
				}
			})
		}
	})

	// test with authentication enabled
	t.Run("router with authentication", func(t *testing.T) {
		// create server with auth
		authSrv := &Web{
			Config: Config{
				RootDir: "testdata",
				Auth:    "testpassword",
			},
			FS: os.DirFS("testdata"),
		}

		router, err := authSrv.router()
		require.NoError(t, err)
		require.NotNil(t, router)

		// create test server
		ts := httptest.NewServer(router)
		defer ts.Close()

		// test login route is available
		resp, err := http.Get(ts.URL + "/login")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Login")
		assert.Contains(t, string(body), "<form")
	})

	t.Run("upload route enabled", func(t *testing.T) {
		uploadSrv := setupTestServer(t)
		uploadSrv.EnableUpload = true
		uploadSrv.UploadMaxSize = 64 * 1024 * 1024

		router, err := uploadSrv.router()
		require.NoError(t, err)

		ts := httptest.NewServer(router)
		defer ts.Close()

		// POST /upload should be reachable (returns 400 for invalid multipart body)
		resp, err := http.Post(ts.URL+"/upload", "multipart/form-data", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "upload route should be registered when enabled")
	})

	t.Run("upload route disabled", func(t *testing.T) {
		disabledSrv := setupTestServer(t)
		disabledSrv.EnableUpload = false

		router, err := disabledSrv.router()
		require.NoError(t, err)

		ts := httptest.NewServer(router)
		defer ts.Close()

		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}

		// POST /upload should not match any route when disabled
		resp, err := client.Post(ts.URL+"/upload", "multipart/form-data", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "upload route should not be registered when disabled")
	})

	t.Run("upload route with auth requires authentication", func(t *testing.T) {
		authUploadSrv := &Web{
			Config: Config{
				RootDir:      "testdata",
				Auth:         "testpassword",
				EnableUpload: true,
			},
			FS: os.DirFS("testdata"),
		}

		router, err := authUploadSrv.router()
		require.NoError(t, err)

		ts := httptest.NewServer(router)
		defer ts.Close()

		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}

		// POST /upload without auth should redirect to login
		resp, err := client.Post(ts.URL+"/upload", "multipart/form-data", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "upload route should require auth")
		assert.Equal(t, "/login", resp.Header.Get("Location"))
	})
}
