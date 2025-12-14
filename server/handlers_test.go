package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRoot(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{name: "root directory", path: "", expectedStatus: http.StatusOK, expectedBody: "file1.txt"},
		{name: "subdirectory", path: "dir1", expectedStatus: http.StatusOK, expectedBody: "file3.txt"},
		{name: "non-existent directory", path: "non-existent", expectedStatus: http.StatusNotFound, expectedBody: "path not found"},
		{name: "file path redirects to download", path: "file1.txt", expectedStatus: http.StatusSeeOther, expectedBody: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/?path="+tc.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(srv.handleRoot)
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code)
			if tc.expectedBody != "" {
				assert.Contains(t, rr.Body.String(), tc.expectedBody)
			}
			if tc.expectedStatus == http.StatusSeeOther {
				assert.Contains(t, rr.Header().Get("Location"), "/file1.txt")
			}
		})
	}
}

func TestHandleDownload(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedHeader map[string]string
		expectedBody   string
	}{
		{
			name:           "download existing file",
			path:           "/file1.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file1.txt\"",
			},
			expectedBody: "file1 content",
		},
		{
			name:           "download file in subdirectory",
			path:           "/dir1/file3.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file3.txt\"",
			},
			expectedBody: "file3 content",
		},
		{
			name:           "download non-existent file",
			path:           "/non-existent.txt",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "file not found",
		},
		{
			name:           "directory redirects to view",
			path:           "/dir1",
			expectedStatus: http.StatusSeeOther,
			expectedHeader: map[string]string{"Location": "/?path=dir1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", tc.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(srv.handleDownload)
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code)

			if tc.expectedBody != "" {
				assert.Contains(t, rr.Body.String(), tc.expectedBody)
			}

			for k, v := range tc.expectedHeader {
				assert.Contains(t, rr.Header().Get(k), v)
			}
		})
	}
}

func TestHandleDirContents(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name            string
		path            string
		sort            string
		dir             string
		expectedStatus  int
		expectedBody    []string
		notExpectedBody []string
	}{
		{
			name: "root directory default sort", path: "", sort: "", dir: "", expectedStatus: http.StatusOK,
			expectedBody: []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name: "root directory sort by name desc", path: "", sort: "name", dir: "desc", expectedStatus: http.StatusOK,
			expectedBody: []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name: "subdirectory", path: "dir1", sort: "", dir: "", expectedStatus: http.StatusOK,
			expectedBody: []string{"file3.txt", "subdir"},
		},
		{
			name: "non-existent directory", path: "non-existent", sort: "", dir: "", expectedStatus: http.StatusNotFound,
			expectedBody: []string{"directory not found"},
		},
		{
			name: "file path is not a directory", path: "file1.txt", sort: "", dir: "", expectedStatus: http.StatusBadRequest,
			expectedBody: []string{"not a directory"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestURL := "/partials/dir-contents?path=" + tc.path
			if tc.sort != "" {
				requestURL += "&sort=" + tc.sort
			}
			if tc.dir != "" {
				requestURL += "&dir=" + tc.dir
			}

			req, err := http.NewRequest("GET", requestURL, nil)
			require.NoError(t, err)
			req.Header.Set("HX-Request", "true") // simulate HTMX request

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(srv.handleDirContents)
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code)

			for _, expected := range tc.expectedBody {
				assert.Contains(t, rr.Body.String(), expected)
			}

			for _, notExpected := range tc.notExpectedBody {
				assert.NotContains(t, rr.Body.String(), notExpected)
			}
		})
	}
}

func TestHandleDirContentsRedirectsNonHTMX(t *testing.T) {
	srv := setupTestServer(t)

	req, err := http.NewRequest("GET", "/partials/dir-contents?path=dir1&sort=name&dir=asc", nil)
	require.NoError(t, err)
	// no HX-Request header set - simulates direct browser access

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(srv.handleDirContents)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "/?path=dir1&sort=name&dir=asc", rr.Header().Get("Location"))
}

func TestTitleFunctionality(t *testing.T) {
	// create a server with a custom title
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	customTitleSrv := &Web{
		Config: Config{
			ListenAddr: ":0",
			Theme:      "light",
			HideFooter: false,
			RootDir:    testdataDir,
			Version:    "test-version",
			Title:      "Custom Title",
		},
		FS: os.DirFS(testdataDir),
	}

	// initialize templates for testing
	err = customTitleSrv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	// test that the title appears in the rendered HTML
	req, err := http.NewRequest("GET", "/", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(customTitleSrv.handleRoot)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// check that the custom title appears in the page
	assert.Contains(t, rr.Body.String(), "Custom Title")
	assert.Contains(t, rr.Body.String(), "<title>Custom Title</title>")

	// also check it's used in the breadcrumb home link
	assert.Contains(t, rr.Body.String(), "Custom Title\n        </a>")

	// test login page also has the title
	req, err = http.NewRequest("GET", "/login", nil)
	require.NoError(t, err)

	rr = httptest.NewRecorder()
	handler = http.HandlerFunc(customTitleSrv.handleLoginPage)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	// check that the title is in the HTML
	assert.Contains(t, rr.Body.String(), "<title>Login - Custom Title</title>")
}

func TestCustomFooter(t *testing.T) {
	// create a server with a custom footer
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	customFooterHTML := "Custom Footer <a href=\"https://example.com\">with a link</a>"

	srv := &Web{
		Config: Config{
			ListenAddr:   ":0",
			Theme:        "light",
			RootDir:      testdataDir,
			CustomFooter: customFooterHTML,
		},
		FS: os.DirFS(testdataDir),
	}

	// initialize templates for testing
	err = srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	// test custom footer appears in the index page
	req, err := http.NewRequest("GET", "/", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(srv.handleRoot)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// check that the custom footer appears in the HTML
	assert.Contains(t, rr.Body.String(), customFooterHTML)

	// check that the default footer links don't appear
	assert.NotContains(t, rr.Body.String(), "https://weblist.umputun.dev")
	assert.NotContains(t, rr.Body.String(), "https://github.com/umputun/weblist")

	// test custom footer appears in the login page too
	req, err = http.NewRequest("GET", "/login", nil)
	require.NoError(t, err)

	rr = httptest.NewRecorder()
	handler = http.HandlerFunc(srv.handleLoginPage)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// check that the custom footer appears in the login page HTML
	assert.Contains(t, rr.Body.String(), customFooterHTML)
}

func TestFileViewAndModal(t *testing.T) {
	srv := setupTestServer(t)

	// test handleViewFile
	t.Run("view text file", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/view/file1.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "text/html", rr.Header().Get("Content-Type"))

		body := rr.Body.String()
		assert.Contains(t, body, "file1 content")     // the actual content from testdata/file1.txt
		assert.Contains(t, body, "highlight-wrapper") // content should be wrapped in highlight-wrapper div
		assert.Contains(t, body, "<!DOCTYPE html>")   // should render with HTML template
	})

	t.Run("view non-existent file", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/view/non-existent.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Body.String(), "file not found")
	})

	t.Run("view directory", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/view/dir1", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "cannot view directories")
	})

	t.Run("excluded file", func(t *testing.T) {
		// create a server with exclusion patterns
		customSrv := &Web{
			Config: Config{
				RootDir: "testdata",
				Exclude: []string{"file1.txt"},
			},
			FS: os.DirFS("testdata"),
		}

		req, err := http.NewRequest("GET", "/view/file1.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(customSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied")
	})

	t.Run("file open error", func(t *testing.T) {
		// create a server with mock filesystem that fails on open
		mockFS := &mockFSWithFiles{
			files: map[string]mockFile{
				"test.txt": {
					name:    "test.txt",
					isDir:   false,
					content: []byte("test content"),
					size:    12,
					modTime: time.Now(),
				},
			},
			failOpen: true,
		}

		mockSrv := &Web{
			Config: Config{},
			FS:     mockFS,
		}

		req, err := http.NewRequest("GET", "/view/test.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(mockSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.Contains(t, rr.Body.String(), "error opening file")
	})

	t.Run("file read error", func(t *testing.T) {
		// create a server with mock filesystem that fails on read
		mockFS := &mockFSWithFiles{
			files: map[string]mockFile{
				"test.txt": {
					name:     "test.txt",
					isDir:    false,
					content:  []byte("test content"),
					size:     12,
					modTime:  time.Now(),
					failRead: true,
				},
			},
		}

		mockSrv := &Web{
			Config: Config{},
			FS:     mockFS,
		}

		req, err := http.NewRequest("GET", "/view/test.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(mockSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.Contains(t, rr.Body.String(), "error reading file")
	})

	t.Run("text content with highlight-wrapper string is escaped", func(t *testing.T) {
		// file contains "highlight-wrapper" as plain text - should be HTML-escaped, not rendered as HTML
		content := `package test
// test file that contains highlight-wrapper string in comments
func TestSomething() {
    expected := "<div class=\"highlight-wrapper\">content</div>"
}`
		mockFS := &mockFSWithFiles{
			files: map[string]mockFile{
				"test.go": {
					name:    "test.go",
					isDir:   false,
					content: []byte(content),
					size:    int64(len(content)),
					modTime: time.Now(),
				},
			},
		}

		mockSrv := &Web{
			Config: Config{EnableSyntaxHighlighting: false},
			FS:     mockFS,
		}
		require.NoError(t, mockSrv.initTemplates())

		req, err := http.NewRequest("GET", "/view/test.go", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(mockSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		body := rr.Body.String()
		// content should be HTML-escaped and wrapped in <pre> tag, not rendered as HTML
		assert.Contains(t, body, "<pre>")
		assert.Contains(t, body, "&lt;div class=")           // HTML-escaped angle brackets
		assert.Contains(t, body, "\\&#34;highlight-wrapper") // HTML-escaped backslash-quote sequence
		assert.NotContains(t, body, "{{ .Content | safe }}")
	})

	t.Run("direct URL uses server default theme", func(t *testing.T) {
		// server configured with dark theme
		darkSrv := &Web{Config: Config{Theme: "dark"}, FS: os.DirFS("testdata")}
		require.NoError(t, darkSrv.initTemplates())

		// request without ?theme= query param should use server's default (dark)
		req, err := http.NewRequest("GET", "/view/file1.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(darkSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		body := rr.Body.String()
		assert.Contains(t, body, `data-theme="dark"`)
	})

	t.Run("query param overrides server default theme", func(t *testing.T) {
		// server configured with dark theme
		darkSrv := &Web{Config: Config{Theme: "dark"}, FS: os.DirFS("testdata")}
		require.NoError(t, darkSrv.initTemplates())

		// explicit ?theme=light should override server's dark theme
		req, err := http.NewRequest("GET", "/view/file1.txt?theme=light", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(darkSrv.handleViewFile)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		body := rr.Body.String()
		assert.Contains(t, body, `data-theme="light"`)
	})

	// test handleFileModal
	t.Run("modal for text file", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/partials/file-modal?path=file1.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleFileModal)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "text/html", rr.Header().Get("Content-Type"))

		body := rr.Body.String()
		assert.Contains(t, body, "file1.txt")  // should contain filename
		assert.Contains(t, body, "<iframe")    // should use iframe for text files
		assert.Contains(t, body, "file-modal") // should use the file-modal class
	})

	t.Run("modal with missing path parameter", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/partials/file-modal", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleFileModal)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "file path not provided")
	})

	t.Run("modal for non-existent file", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/partials/file-modal?path=non-existent.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleFileModal)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Body.String(), "file not found")
	})

	t.Run("modal for directory", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/partials/file-modal?path=dir1", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleFileModal)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "cannot display directories in modal")
	})

	t.Run("excluded file in modal", func(t *testing.T) {
		// create a server with exclusion patterns
		customSrv := &Web{
			Config: Config{
				RootDir: "testdata",
				Exclude: []string{"file1.txt"},
			},
			FS: os.DirFS("testdata"),
		}

		req, err := http.NewRequest("GET", "/partials/file-modal?path=file1.txt", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(customSrv.handleFileModal)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied")
	})
}

func TestDirectoryPathRedirection(t *testing.T) {
	// set up a test server
	srv := setupTestServer(t)

	// create a test HTTP server
	ts := httptest.NewServer(http.HandlerFunc(srv.handleDownload))
	defer ts.Close()

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedPath   string
	}{
		{name: "nested directory redirects to view", path: "/dir1", expectedStatus: http.StatusSeeOther, expectedPath: "/?path=dir1"},
		{name: "nested directory with trailing slash", path: "/dir1/", expectedStatus: http.StatusSeeOther, expectedPath: "/?path=dir1"},
		{name: "deeply nested directory", path: "/dir1/subdir", expectedStatus: http.StatusSeeOther, expectedPath: "/?path=dir1/subdir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// create a request using path from the test case
			req, err := http.NewRequest("GET", tc.path, nil)
			require.NoError(t, err)

			// create a response recorder
			rr := httptest.NewRecorder()

			// call the handler
			srv.handleDownload(rr, req)

			// check the status code
			assert.Equal(t, tc.expectedStatus, rr.Code, "Status code should match")

			// check that we're redirected to the right path
			location := rr.Header().Get("Location")
			assert.Equal(t, tc.expectedPath, location, "Redirect location should match")
		})
	}
}

func TestHTMLRendering(t *testing.T) {
	// create a temporary HTML file for testing
	htmlContent := `<!DOCTYPE html>
<html>
<head>
	<title>Test HTML</title>
	<style>body { color: red; }</style>
</head>
<body>
	<h1>Test HTML Content</h1>
	<p>This is a test paragraph.</p>
</body>
</html>`

	// create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "weblist-test-html")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// create the test HTML file
	htmlFilePath := filepath.Join(tmpDir, "test.html")
	err = os.WriteFile(htmlFilePath, []byte(htmlContent), 0644)
	require.NoError(t, err)

	// set up a test server with the temp directory
	srv := &Web{
		Config: Config{
			Theme: "light",
		},
		FS: os.DirFS(tmpDir),
	}

	// initialize templates for testing
	err = srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	t.Run("html file view renders correctly", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/view/test.html", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleViewFile)
		handler.ServeHTTP(rr, req)

		// check status code
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "text/html", rr.Header().Get("Content-Type"))

		body := rr.Body.String()
		// verify it doesn't use pre tags for HTML
		assert.NotContains(t, body, "<pre>"+htmlContent+"</pre>")
		// verify it uses the html-content div
		assert.Contains(t, body, `<div class="html-content">`)
		// verify the HTML content is rendered
		assert.Contains(t, body, "<h1>Test HTML Content</h1>")
		assert.Contains(t, body, "<p>This is a test paragraph.</p>")
	})

	t.Run("html file modal references iframe with correct path", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/partials/file-modal?path=test.html", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleFileModal)
		handler.ServeHTTP(rr, req)

		// check status code
		assert.Equal(t, http.StatusOK, rr.Code)

		body := rr.Body.String()
		// check that we're using the iframe for HTML files
		assert.Contains(t, body, `<iframe src="/view/test.html?theme=light"`)
		// check that the sandbox attribute is set correctly
		assert.Contains(t, body, `sandbox="allow-same-origin allow-scripts allow-forms"`)
	})
}

func TestAPIList_Contents(t *testing.T) {
	testDir := "./testdata"
	testFS := os.DirFS(testDir)

	web := &Web{
		Config: Config{
			RootDir: testDir,
			Exclude: []string{".DS_Store"}, // exclude macOS files
		},
		FS: testFS,
	}

	t.Run("root directory listing", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/list?path=.", nil)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		web.handleAPIList(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var response struct {
			Path  string `json:"path"`
			Files []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
			} `json:"files"`
		}

		err = json.NewDecoder(rec.Body).Decode(&response)
		require.NoError(t, err)

		assert.Equal(t, "", response.Path)

		// check that expected directories and files are present
		expectedFiles := map[string]bool{
			"dir1":      true,
			"dir2":      true,
			"empty-dir": true,
			"file1.txt": true,
			"file2.txt": true,
		}

		foundFiles := make(map[string]bool)
		dirCount := 0
		fileCount := 0

		for _, file := range response.Files {
			foundFiles[file.Name] = true
			if file.IsDir {
				dirCount++
			} else {
				fileCount++
			}
		}

		for name := range expectedFiles {
			assert.True(t, foundFiles[name], "expected file %s not found in response", name)
		}

		assert.Equal(t, 3, dirCount, "unexpected number of directories")
		assert.Equal(t, 2, fileCount, "unexpected number of files")
	})

	t.Run("subdirectory listing", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/list?path=dir1", nil)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		web.handleAPIList(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var response struct {
			Path  string `json:"path"`
			Files []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
			} `json:"files"`
		}

		err = json.NewDecoder(rec.Body).Decode(&response)
		require.NoError(t, err)

		assert.Equal(t, "dir1", response.Path)

		// check that expected entries are present
		expectedFiles := map[string]bool{
			"..":        true,
			"subdir":    true,
			"file3.txt": true,
		}

		for _, file := range response.Files {
			assert.True(t, expectedFiles[file.Name], "unexpected file %s in response", file.Name)
		}
	})
}

func TestAPIList_Sort(t *testing.T) {
	tmpDir := t.TempDir()

	// create subdirectories
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "adir"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "bdir"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "cdir"), 0755))

	// create files with different sizes
	smallFile := filepath.Join(tmpDir, "small.txt")
	mediumFile := filepath.Join(tmpDir, "medium.txt")
	largeFile := filepath.Join(tmpDir, "large.txt")

	require.NoError(t, os.WriteFile(smallFile, []byte("test"), 0644))
	require.NoError(t, os.WriteFile(mediumFile, []byte("test content"), 0644))
	require.NoError(t, os.WriteFile(largeFile, []byte("this is a larger test content file"), 0644))

	// set different modification times
	oldTime := time.Now().Add(-24 * time.Hour)
	mediumTime := time.Now().Add(-12 * time.Hour)
	recentTime := time.Now().Add(-1 * time.Hour)

	require.NoError(t, os.Chtimes(smallFile, oldTime, oldTime))
	require.NoError(t, os.Chtimes(mediumFile, mediumTime, mediumTime))
	require.NoError(t, os.Chtimes(largeFile, recentTime, recentTime))

	web := &Web{
		Config: Config{
			RootDir: tmpDir,
		},
		FS: os.DirFS(tmpDir),
	}

	tests := []struct {
		name      string
		sortParam string
		wantSort  string
		wantDir   string
	}{
		{name: "default sort", sortParam: "", wantSort: "name", wantDir: "asc"},
		{name: "sort by name asc", sortParam: "+name", wantSort: "name", wantDir: "asc"},
		{name: "sort by name desc", sortParam: "-name", wantSort: "name", wantDir: "desc"},
		{name: "sort by size asc", sortParam: "+size", wantSort: "size", wantDir: "asc"},
		{name: "sort by size desc", sortParam: "-size", wantSort: "size", wantDir: "desc"},
		{name: "sort by mtime asc", sortParam: "+mtime", wantSort: "date", wantDir: "asc"},
		{name: "sort by mtime desc", sortParam: "-mtime", wantSort: "date", wantDir: "desc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestURL := "/api/list?path=."
			if tt.sortParam != "" {
				requestURL += "&sort=" + tt.sortParam
			}

			req, err := http.NewRequest("GET", requestURL, nil)
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			web.handleAPIList(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)

			var response struct {
				Sort  string `json:"sort"`
				Dir   string `json:"dir"`
				Files []struct {
					Name string `json:"name"`
				} `json:"files"`
			}

			err = json.NewDecoder(rec.Body).Decode(&response)
			require.NoError(t, err)

			assert.Equal(t, tt.wantSort, response.Sort, "incorrect sort field")
			assert.Equal(t, tt.wantDir, response.Dir, "incorrect sort direction")
		})
	}

	t.Run("name ascending order", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/list?path=.&sort=+name", nil)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		web.handleAPIList(rec, req)

		var response struct {
			Files []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
			} `json:"files"`
		}

		err = json.NewDecoder(rec.Body).Decode(&response)
		require.NoError(t, err)

		// dirs should come first in alphabetical order, then files
		dirNames := []string{}
		fileNames := []string{}

		for _, file := range response.Files {
			if file.IsDir {
				dirNames = append(dirNames, file.Name)
			} else {
				fileNames = append(fileNames, file.Name)
			}
		}

		// check directory order is alphabetical
		assert.Equal(t, []string{"adir", "bdir", "cdir"}, dirNames)

		// check file order is alphabetical
		assert.Equal(t, []string{"large.txt", "medium.txt", "small.txt"}, fileNames)
	})
}

func TestAPIList_ErrorCases(t *testing.T) {
	testDir := "./testdata"
	testFS := os.DirFS(testDir)

	web := &Web{
		Config: Config{
			RootDir: testDir,
			Exclude: []string{".DS_Store"}, // exclude macOS files
		},
		FS: testFS,
	}

	t.Run("non-existent directory", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/list?path=non-existent-dir", nil)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		web.handleAPIList(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var response map[string]string
		err = json.NewDecoder(rec.Body).Decode(&response)
		require.NoError(t, err)

		assert.Contains(t, response["error"], "directory not found")
	})

	t.Run("file instead of directory", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/list?path=file1.txt", nil)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		web.handleAPIList(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var response map[string]string
		err = json.NewDecoder(rec.Body).Decode(&response)
		require.NoError(t, err)

		assert.Equal(t, "not a directory", response["error"])
	})
}

func TestHandleSelectionStatus(t *testing.T) {
	web := &Web{
		Config: Config{
			RootDir: "testdata",
			Theme:   "light",
		},
		FS: os.DirFS("testdata"),
	}

	// initialize templates for testing
	err := web.initTemplates()
	require.NoError(t, err)

	t.Run("no files selected", func(t *testing.T) {
		formData := url.Values{}
		req := httptest.NewRequest("POST", "/partials/selection-status", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleSelectionStatus(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		require.NotContains(t, rr.Body.String(), "Download Selected")
		assert.Empty(t, rr.Header().Get("HX-Trigger"))
	})

	t.Run("multiple files selected", func(t *testing.T) {
		formData := url.Values{}
		formData.Add("selected-files", "file1.txt")
		formData.Add("selected-files", "file2.txt")
		req := httptest.NewRequest("POST", "/partials/selection-status", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleSelectionStatus(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), "Download Selected")
		require.Contains(t, rr.Body.String(), "2 files selected")
		assert.Empty(t, rr.Header().Get("HX-Trigger"))
	})

	t.Run("select-all with invalid total-files", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("select-all", "true")
		formData.Set("total-files", "invalid")
		req := httptest.NewRequest("POST", "/partials/selection-status", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleSelectionStatus(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "Invalid total-files value")
	})

	t.Run("select-all toggles from none to all", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("select-all", "true")
		formData.Set("total-files", "3")
		formData.Add("path-values", "file1.txt")
		formData.Add("path-values", "file2.txt")
		formData.Add("path-values", "file3.txt")
		req := httptest.NewRequest("POST", "/partials/selection-status", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleSelectionStatus(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "updateCheckboxes", rr.Header().Get("HX-Trigger"))
		require.Contains(t, rr.Body.String(), "3 files selected")
	})

	t.Run("select-all toggles from all to none", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("select-all", "true")
		formData.Set("total-files", "2")
		formData.Add("selected-files", "file1.txt")
		formData.Add("selected-files", "file2.txt")
		formData.Add("path-values", "file1.txt")
		formData.Add("path-values", "file2.txt")
		req := httptest.NewRequest("POST", "/partials/selection-status", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleSelectionStatus(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "updateCheckboxes", rr.Header().Get("HX-Trigger"))
		require.NotContains(t, rr.Body.String(), "files selected")
	})
}

func TestHandleDownloadSelected(t *testing.T) {
	// create test web server with testdata
	web := &Web{
		Config: Config{
			RootDir: "testdata",
		},
		FS: os.DirFS("testdata"),
	}

	t.Run("Multiple files selection", func(t *testing.T) {
		// test with multiple files selected
		formData := url.Values{}
		formData.Add("selected-files", "file1.txt")
		formData.Add("selected-files", "file2.txt")
		req := httptest.NewRequest("POST", "/download-selected", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleDownloadSelected(rr, req)

		// verify response headers
		assert.Equal(t, "application/zip", rr.Header().Get("Content-Type"))
		assert.Contains(t, rr.Header().Get("Content-Disposition"), "attachment; filename=\"weblist-files-")

		// verify ZIP file content
		reader := bytes.NewReader(rr.Body.Bytes())
		zipReader, err := zip.NewReader(reader, int64(len(rr.Body.Bytes())))
		require.NoError(t, err)

		// check that the ZIP contains the expected files
		fileNames := make([]string, 0, len(zipReader.File))
		for _, zipFile := range zipReader.File {
			fileNames = append(fileNames, zipFile.Name)
		}
		assert.ElementsMatch(t, []string{"file1.txt", "file2.txt"}, fileNames)

		// check file content
		var file1Found bool
		for _, zipFile := range zipReader.File {
			if zipFile.Name != "file1.txt" {
				continue
			}

			file1Found = true
			f, err := zipFile.Open()
			require.NoError(t, err)

			content, err := io.ReadAll(f)
			require.NoError(t, err)
			// the test file might have different content in testdata
			assert.NotEmpty(t, content)
			f.Close() // close explicitly instead of using defer in a loop
		}

		// ensure we found and checked file1.txt
		assert.True(t, file1Found, "file1.txt should be in the ZIP archive")
	})

	t.Run("Directory selection with recursive content", func(t *testing.T) {
		formData := url.Values{}
		formData.Add("selected-files", "dir1") // this directory contains file3.txt and subdir/file4.txt
		req := httptest.NewRequest("POST", "/download-selected", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleDownloadSelected(rr, req)

		// verify response headers
		assert.Equal(t, "application/zip", rr.Header().Get("Content-Type"))
		assert.Contains(t, rr.Header().Get("Content-Disposition"), "attachment; filename=\"weblist-files-")

		// verify ZIP file content
		reader := bytes.NewReader(rr.Body.Bytes())
		zipReader, err := zip.NewReader(reader, int64(len(rr.Body.Bytes())))
		require.NoError(t, err)

		// extract all file paths from the ZIP
		var zipPaths []string
		for _, zipFile := range zipReader.File {
			zipPaths = append(zipPaths, zipFile.Name)
		}

		// verify that we have the expected directory structure
		assert.Contains(t, zipPaths, "file3.txt", "file3.txt should be in the ZIP")
		assert.Contains(t, zipPaths, "subdir/file4.txt", "subdir/file4.txt should be in the ZIP")

		// verify directory entry exists
		assert.True(t, slices.Contains(zipPaths, "subdir/"), "directory entry for subdir/ should exist in ZIP")
	})

	t.Run("Mixed selection of files and directories", func(t *testing.T) {
		formData := url.Values{}
		formData.Add("selected-files", "file1.txt")
		formData.Add("selected-files", "dir1") // this directory contains file3.txt and subdir/file4.txt
		req := httptest.NewRequest("POST", "/download-selected", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		web.handleDownloadSelected(rr, req)

		// verify ZIP file content
		reader := bytes.NewReader(rr.Body.Bytes())
		zipReader, err := zip.NewReader(reader, int64(len(rr.Body.Bytes())))
		require.NoError(t, err)

		// extract all file paths from the ZIP
		var zipPaths []string
		for _, zipFile := range zipReader.File {
			zipPaths = append(zipPaths, zipFile.Name)
		}

		// verify that we have the expected files
		assert.Contains(t, zipPaths, "file1.txt", "file1.txt should be in the ZIP")
		assert.Contains(t, zipPaths, "file3.txt", "file3.txt should be in the ZIP")
		assert.Contains(t, zipPaths, "subdir/file4.txt", "subdir/file4.txt should be in the ZIP")
	})
}
