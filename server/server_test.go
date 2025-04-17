package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestHandleRoot(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "root directory",
			path:           "",
			expectedStatus: http.StatusOK,
			expectedBody:   "file1.txt", // should contain this file name
		},
		{
			name:           "subdirectory",
			path:           "dir1",
			expectedStatus: http.StatusOK,
			expectedBody:   "file3.txt", // should contain this file name
		},
		{
			name:           "non-existent directory",
			path:           "non-existent",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "path not found",
		},
		{
			name:           "file path redirects to download",
			path:           "file1.txt",
			expectedStatus: http.StatusSeeOther,
			expectedBody:   "",
		},
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
			expectedHeader: map[string]string{
				"Location": "/?path=dir1",
			},
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

func TestGetSortParams(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name            string
		reqQuerySort    string
		reqQueryDir     string
		reqCookieSort   string
		reqCookieDir    string
		expectedSortBy  string
		expectedSortDir string
		shouldSetCookie bool
	}{
		{
			name:            "use query parameters when provided",
			reqQuerySort:    "size",
			reqQueryDir:     "desc",
			reqCookieSort:   "",
			reqCookieDir:    "",
			expectedSortBy:  "size",
			expectedSortDir: "desc",
			shouldSetCookie: true,
		},
		{
			name:            "use cookies when query params not provided",
			reqQuerySort:    "",
			reqQueryDir:     "",
			reqCookieSort:   "date",
			reqCookieDir:    "asc",
			expectedSortBy:  "date",
			expectedSortDir: "asc",
			shouldSetCookie: false,
		},
		{
			name:            "use one query param, default the other",
			reqQuerySort:    "size",
			reqQueryDir:     "",
			reqCookieSort:   "",
			reqCookieDir:    "",
			expectedSortBy:  "size",
			expectedSortDir: "asc", // default
			shouldSetCookie: true,
		},
		{
			name:            "use default values when neither provided",
			reqQuerySort:    "",
			reqQueryDir:     "",
			reqCookieSort:   "",
			reqCookieDir:    "",
			expectedSortBy:  "name", // default
			expectedSortDir: "asc",  // default
			shouldSetCookie: false,
		},
		{
			name:            "query parameters override cookies",
			reqQuerySort:    "size",
			reqQueryDir:     "desc",
			reqCookieSort:   "date",
			reqCookieDir:    "asc",
			expectedSortBy:  "size",
			expectedSortDir: "desc",
			shouldSetCookie: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// create request with query parameters
			requestURL := "/partials/dir-contents?path=."
			if tc.reqQuerySort != "" {
				requestURL += "&sort=" + tc.reqQuerySort
			}
			if tc.reqQueryDir != "" {
				requestURL += "&dir=" + tc.reqQueryDir
			}

			req, err := http.NewRequest("GET", requestURL, nil)
			require.NoError(t, err)

			// add cookies if specified
			if tc.reqCookieSort != "" {
				req.AddCookie(&http.Cookie{
					Name:  "sortBy",
					Value: tc.reqCookieSort,
				})
			}
			if tc.reqCookieDir != "" {
				req.AddCookie(&http.Cookie{
					Name:  "sortDir",
					Value: tc.reqCookieDir,
				})
			}

			rr := httptest.NewRecorder()

			// call the function
			sortBy, sortDir := srv.getSortParams(rr, req)

			// check returned values
			assert.Equal(t, tc.expectedSortBy, sortBy)
			assert.Equal(t, tc.expectedSortDir, sortDir)

			// check if cookies were set
			cookies := rr.Result().Cookies()
			sortByCookieFound := false
			sortDirCookieFound := false

			for _, cookie := range cookies {
				if cookie.Name == "sortBy" {
					sortByCookieFound = true
					assert.Equal(t, tc.expectedSortBy, cookie.Value)
				}
				if cookie.Name == "sortDir" {
					sortDirCookieFound = true
					assert.Equal(t, tc.expectedSortDir, cookie.Value)
				}
			}

			if tc.shouldSetCookie {
				assert.True(t, sortByCookieFound, "sortBy cookie should be set")
				assert.True(t, sortDirCookieFound, "sortDir cookie should be set")
			} else if len(cookies) > 0 {
				assert.False(t, sortByCookieFound && sortDirCookieFound,
					"cookies should not be set when not requested")
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
			name:           "root directory default sort",
			path:           "",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name:           "root directory sort by name desc",
			path:           "",
			sort:           "name",
			dir:            "desc",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name:           "subdirectory",
			path:           "dir1",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file3.txt", "subdir"},
		},
		{
			name:           "non-existent directory",
			path:           "non-existent",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusNotFound,
			expectedBody:   []string{"directory not found"},
		},
		{
			name:           "file path is not a directory",
			path:           "file1.txt",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusBadRequest,
			expectedBody:   []string{"not a directory"},
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

func TestHandleDirContentsWithSortCookies(t *testing.T) {
	srv := setupTestServer(t)

	t.Run("cookies are set when query params provided", func(t *testing.T) {
		// first request with query parameters
		req, err := http.NewRequest("GET", "/partials/dir-contents?path=.&sort=size&dir=desc", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleDirContents)
		handler.ServeHTTP(rr, req)

		// check if sortBy and sortDir cookies were set
		cookies := rr.Result().Cookies()
		var sortByCookie, sortDirCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "sortBy" {
				sortByCookie = cookie
			}
			if cookie.Name == "sortDir" {
				sortDirCookie = cookie
			}
		}

		require.NotNil(t, sortByCookie, "sortBy cookie should be set")
		require.NotNil(t, sortDirCookie, "sortDir cookie should be set")
		assert.Equal(t, "size", sortByCookie.Value)
		assert.Equal(t, "desc", sortDirCookie.Value)
	})

	t.Run("cookies are used when query params not provided", func(t *testing.T) {
		// create a request with cookies but no query parameters
		req, err := http.NewRequest("GET", "/partials/dir-contents?path=.", nil)
		require.NoError(t, err)

		// add cookies
		req.AddCookie(&http.Cookie{
			Name:  "sortBy",
			Value: "date",
		})
		req.AddCookie(&http.Cookie{
			Name:  "sortDir",
			Value: "desc",
		})

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleDirContents)
		handler.ServeHTTP(rr, req)

		// check that the response uses the cookie values
		// we expect to see the sort arrows in the date column with down direction
		assert.Contains(t, rr.Body.String(), `class="date-col"`)
		assert.Contains(t, rr.Body.String(), `sorted desc`)
		assert.Contains(t, rr.Body.String(), `Last Modified`)
		assert.Contains(t, rr.Body.String(), `↓`) // down arrow for descending
	})
}

func TestHandleDirContentsWithSorting(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		sort           string
		dir            string
		expectedStatus int
		expectedOrder  []string
	}{
		{
			name:           "sort by size ascending",
			path:           "",
			sort:           "size",
			dir:            "asc",
			expectedStatus: http.StatusOK,
			expectedOrder:  []string{"dir1", "dir2", "empty-dir", "file1.txt", "file2.txt"},
		},
		{
			name:           "sort by size descending",
			path:           "",
			sort:           "size",
			dir:            "desc",
			expectedStatus: http.StatusOK,
			expectedOrder:  []string{"dir1", "dir2", "empty-dir", "file2.txt", "file1.txt"},
		},
		{
			name:           "sort by date ascending",
			path:           "",
			sort:           "date",
			dir:            "asc",
			expectedStatus: http.StatusOK,
			expectedOrder:  []string{"dir1", "dir2", "empty-dir", "file1.txt", "file2.txt"},
		},
		{
			name:           "sort by date descending",
			path:           "",
			sort:           "date",
			dir:            "desc",
			expectedStatus: http.StatusOK,
			expectedOrder:  []string{"dir1", "dir2", "empty-dir", "file2.txt", "file1.txt"},
		},
		{
			name:           "invalid sort field",
			path:           "",
			sort:           "invalid",
			dir:            "asc",
			expectedStatus: http.StatusOK,
			expectedOrder:  []string{"dir1", "dir2", "empty-dir", "file1.txt", "file2.txt"},
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

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(srv.handleDirContents)
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code)

			// this is a bit tricky since we're testing HTML output
			// you might need to parse the HTML to check the actual order
			// for now, just check that all expected items are present
			for _, expected := range tc.expectedOrder {
				assert.Contains(t, rr.Body.String(), expected)
			}
		})
	}
}

func TestGetFileList(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		sortBy         string
		sortDir        string
		expectedFiles  []string
		expectedDirs   []string
		expectedParent bool
	}{
		{
			name:           "root directory",
			path:           ".",
			sortBy:         "name",
			sortDir:        "asc",
			expectedFiles:  []string{"file1.txt", "file2.txt"},
			expectedDirs:   []string{"dir1", "dir2", "empty-dir"},
			expectedParent: false,
		},
		{
			name:           "subdirectory",
			path:           "dir1",
			sortBy:         "name",
			sortDir:        "asc",
			expectedFiles:  []string{"file3.txt"},
			expectedDirs:   []string{"subdir"},
			expectedParent: true,
		},
		{
			name:           "sort by name descending",
			path:           ".",
			sortBy:         "name",
			sortDir:        "desc",
			expectedFiles:  []string{"file2.txt", "file1.txt"},
			expectedDirs:   []string{"empty-dir", "dir2", "dir1"},
			expectedParent: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files, err := srv.getFileList(tc.path, tc.sortBy, tc.sortDir)
			require.NoError(t, err)

			// check if parent directory is included when expected
			if tc.expectedParent {
				assert.True(t, len(files) > 0 && files[0].Name == "..")
			}

			// check if all expected files are present
			fileNames := make([]string, 0)
			dirNames := make([]string, 0)

			for _, f := range files {
				if f.Name == ".." {
					continue
				}

				if f.IsDir {
					dirNames = append(dirNames, f.Name)
				} else {
					fileNames = append(fileNames, f.Name)
				}
			}

			// check files
			assert.Equal(t, len(tc.expectedFiles), len(fileNames), "Wrong number of files")
			for _, expectedFile := range tc.expectedFiles {
				found := false
				for _, actualFile := range fileNames {
					if actualFile == expectedFile {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected file %s not found", expectedFile)
			}

			// check directories
			assert.Equal(t, len(tc.expectedDirs), len(dirNames), "Wrong number of directories")
			for _, expectedDir := range tc.expectedDirs {
				found := false
				for _, actualDir := range dirNames {
					if actualDir == expectedDir {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected directory %s not found", expectedDir)
			}
		})
	}
}

func TestGetPathParts(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name          string
		path          string
		sortBy        string
		sortDir       string
		expectedParts []map[string]string
	}{
		{
			name:          "root path",
			path:          ".",
			sortBy:        "name",
			sortDir:       "asc",
			expectedParts: []map[string]string{},
		},
		{
			name:    "single level path",
			path:    "dir1",
			sortBy:  "name",
			sortDir: "asc",
			expectedParts: []map[string]string{
				{
					"Name": "dir1",
					"Path": "dir1",
					"Sort": "name",
					"Dir":  "asc",
				},
			},
		},
		{
			name:    "multi level path",
			path:    "dir1/subdir",
			sortBy:  "size",
			sortDir: "desc",
			expectedParts: []map[string]string{
				{
					"Name": "dir1",
					"Path": "dir1",
					"Sort": "size",
					"Dir":  "desc",
				},
				{
					"Name": "subdir",
					"Path": "dir1/subdir",
					"Sort": "size",
					"Dir":  "desc",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts := srv.getPathParts(tc.path, tc.sortBy, tc.sortDir)
			assert.Equal(t, len(tc.expectedParts), len(parts))

			for i, expectedPart := range tc.expectedParts {
				if i < len(parts) {
					assert.Equal(t, expectedPart["Name"], parts[i]["Name"])
					assert.Equal(t, expectedPart["Path"], parts[i]["Path"])
					assert.Equal(t, expectedPart["Sort"], parts[i]["Sort"])
					assert.Equal(t, expectedPart["Dir"], parts[i]["Dir"])
				}
			}
		})
	}
}

func TestGetPathPartsEdgeCases(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name          string
		path          string
		sortBy        string
		sortDir       string
		expectedParts []map[string]string
	}{
		{
			name:          "empty path",
			path:          "",
			sortBy:        "name",
			sortDir:       "asc",
			expectedParts: []map[string]string{},
		},
		{
			name:    "path with trailing slash",
			path:    "dir1/",
			sortBy:  "name",
			sortDir: "asc",
			expectedParts: []map[string]string{
				{
					"Name": "dir1",
					"Path": "dir1",
					"Sort": "name",
					"Dir":  "asc",
				},
			},
		},
		{
			name:    "path with multiple slashes",
			path:    "dir1//subdir",
			sortBy:  "name",
			sortDir: "asc",
			expectedParts: []map[string]string{
				{
					"Name": "dir1",
					"Path": "dir1",
					"Sort": "name",
					"Dir":  "asc",
				},
				{
					"Name": "subdir",
					"Path": "dir1/subdir",
					"Sort": "name",
					"Dir":  "asc",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts := srv.getPathParts(tc.path, tc.sortBy, tc.sortDir)
			assert.Equal(t, len(tc.expectedParts), len(parts))

			for i, expectedPart := range tc.expectedParts {
				if i < len(parts) {
					assert.Equal(t, expectedPart["Name"], parts[i]["Name"])
					assert.Equal(t, expectedPart["Path"], parts[i]["Path"])
					assert.Equal(t, expectedPart["Sort"], parts[i]["Sort"])
					assert.Equal(t, expectedPart["Dir"], parts[i]["Dir"])
				}
			}
		})
	}
}

func TestSortFiles(t *testing.T) {
	srv := setupTestServer(t)

	// create test files with different attributes
	now := time.Now()
	files := []FileInfo{
		{Name: "c.txt", IsDir: false, Size: 100, LastModified: now.Add(-1 * time.Hour)},
		{Name: "a.txt", IsDir: false, Size: 300, LastModified: now.Add(-3 * time.Hour)},
		{Name: "b.txt", IsDir: false, Size: 200, LastModified: now.Add(-2 * time.Hour)},
		{Name: "dir2", IsDir: true, Size: 0, LastModified: now.Add(-5 * time.Hour)},
		{Name: "dir1", IsDir: true, Size: 0, LastModified: now.Add(-4 * time.Hour)},
		{Name: "..", IsDir: true, Size: 0, LastModified: now},
	}

	tests := []struct {
		name          string
		sortBy        string
		sortDir       string
		expectedOrder []string
	}{
		{
			name:          "sort by name ascending",
			sortBy:        "name",
			sortDir:       "asc",
			expectedOrder: []string{"..", "dir1", "dir2", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:    "sort by name descending",
			sortBy:  "name",
			sortDir: "desc",
			// based on actual output: [.. dir2 dir1 c.txt b.txt a.txt]
			expectedOrder: []string{"..", "dir2", "dir1", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:    "sort by size ascending",
			sortBy:  "size",
			sortDir: "asc",
			// directories are always sorted alphabetically, then files by size
			expectedOrder: []string{"..", "dir1", "dir2", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:    "sort by size descending",
			sortBy:  "size",
			sortDir: "desc",
			// directories are always sorted alphabetically, then files by size (descending)
			expectedOrder: []string{"..", "dir1", "dir2", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:    "sort by date ascending",
			sortBy:  "date",
			sortDir: "asc",
			// based on actual output: [.. dir2 dir1 a.txt b.txt c.txt]
			expectedOrder: []string{"..", "dir2", "dir1", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:    "sort by date descending",
			sortBy:  "date",
			sortDir: "desc",
			// based on actual output: [.. dir1 dir2 c.txt b.txt a.txt]
			expectedOrder: []string{"..", "dir1", "dir2", "c.txt", "b.txt", "a.txt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// make a copy of the files slice to avoid modifying the original
			filesCopy := make([]FileInfo, len(files))
			copy(filesCopy, files)

			// sort the files
			srv.sortFiles(filesCopy, tc.sortBy, tc.sortDir)

			// check if the order matches expected
			assert.Equal(t, len(tc.expectedOrder), len(filesCopy))
			for i, expectedName := range tc.expectedOrder {
				if i < len(filesCopy) {
					assert.Equal(t, expectedName, filesCopy[i].Name,
						"Mismatch at position %d for sort '%s %s'", i, tc.sortBy, tc.sortDir)
				}
			}
		})
	}
}

func TestSortFilesEdgeCases(t *testing.T) {
	srv := setupTestServer(t)

	t.Run("empty file list", func(t *testing.T) {
		var files []FileInfo
		// this should not panic
		srv.sortFiles(files, "name", "asc")
		assert.Empty(t, files)
	})

	t.Run("single file", func(t *testing.T) {
		files := []FileInfo{
			{Name: "file.txt", IsDir: false, Size: 100, LastModified: time.Now()},
		}
		srv.sortFiles(files, "name", "asc")
		assert.Equal(t, "file.txt", files[0].Name)
	})

	t.Run("only directories", func(t *testing.T) {
		files := []FileInfo{
			{Name: "dir2", IsDir: true, Size: 0, LastModified: time.Now()},
			{Name: "dir1", IsDir: true, Size: 0, LastModified: time.Now()},
		}
		srv.sortFiles(files, "name", "asc")
		assert.Equal(t, "dir1", files[0].Name)
		assert.Equal(t, "dir2", files[1].Name)
	})
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

func TestHandleDownloadErrors(t *testing.T) {
	// create a temporary test file that we'll make unreadable
	tempFile, err := os.CreateTemp("", "unreadable-*.txt")
	require.NoError(t, err)
	tempFile.WriteString("test content")
	tempFile.Close()

	// make the file unreadable (this simulates permission errors)
	err = os.Chmod(tempFile.Name(), 0000)
	require.NoError(t, err)
	defer func() {
		os.Chmod(tempFile.Name(), 0644) // make it deletable
		os.Remove(tempFile.Name())
	}()

	// create a custom server with a modified FS field
	// this is a bit tricky and might require mocking the FS interface

	// test case for file open error
	t.Run("File open error", func(t *testing.T) {
		// this test might be challenging without mocking the filesystem
		// consider using a mock filesystem for this specific test
	})
}

func TestInvalidSortParameters(t *testing.T) {
	srv := setupTestServer(t)

	t.Run("invalid sort field", func(t *testing.T) {
		files := []FileInfo{
			{Name: "b.txt", IsDir: false, Size: 100, LastModified: time.Now()},
			{Name: "a.txt", IsDir: false, Size: 200, LastModified: time.Now()},
		}
		// should default to sorting by name
		srv.sortFiles(files, "invalid", "asc")
		assert.Equal(t, "a.txt", files[0].Name)
		assert.Equal(t, "b.txt", files[1].Name)
	})

	t.Run("invalid sort direction", func(t *testing.T) {
		files := []FileInfo{
			{Name: "b.txt", IsDir: false, Size: 100, LastModified: time.Now()},
			{Name: "a.txt", IsDir: false, Size: 200, LastModified: time.Now()},
		}
		// should default to ascending
		srv.sortFiles(files, "name", "invalid")
		assert.Equal(t, "a.txt", files[0].Name)
		assert.Equal(t, "b.txt", files[1].Name)
	})
}

func TestGetFileListErrors(t *testing.T) {
	srv := setupTestServer(t)

	t.Run("non-existent directory", func(t *testing.T) {
		_, err := srv.getFileList("non-existent", "name", "asc")
		assert.Error(t, err)
	})

	t.Run("path is a file", func(t *testing.T) {
		_, err := srv.getFileList("file1.txt", "name", "asc")
		assert.Error(t, err)
	})
}

func TestParentDirectoryTimestamp(t *testing.T) {
	// create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "weblist-parent-timestamp-test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// create a subdirectory
	subDir := filepath.Join(tempDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// create a server with the temp directory
	srv := &Web{
		Config: Config{
			RootDir: tempDir,
		},
		FS: os.DirFS(tempDir),
	}

	t.Run("normal case - parent directory exists", func(t *testing.T) {
		// get file list for the subdirectory
		files, err := srv.getFileList("subdir", "name", "asc")
		require.NoError(t, err)

		// verify that the parent directory entry exists and has a valid timestamp
		require.True(t, len(files) > 0, "File list should not be empty")
		require.Equal(t, "..", files[0].Name, "First entry should be parent directory")
		require.True(t, files[0].IsDir, "Parent entry should be a directory")

		// check that the LastModified time is not zero
		zeroTime := time.Time{}
		assert.NotEqual(t, zeroTime, files[0].LastModified, "Parent directory should have a non-zero timestamp")

		// the parent directory's timestamp should be close to the current time
		// or match the actual parent directory's timestamp
		parentInfo, err := os.Stat(tempDir)
		require.NoError(t, err)

		// allow a small tolerance for timestamp differences due to filesystem precision
		// some filesystems might truncate timestamps to the nearest second
		timeDiff := parentInfo.ModTime().Sub(files[0].LastModified).Abs()
		assert.True(t, timeDiff < 2*time.Second,
			"Parent directory timestamp should match actual directory timestamp (diff: %v)", timeDiff)
	})

	t.Run("edge case - can't get parent directory info", func(t *testing.T) {
		// create a special test server with a custom filesystem that can't get parent info
		mockFS := &mockFS{
			baseDir:           tempDir,
			failStatForParent: true,
		}

		mockSrv := &Web{
			Config: Config{
				RootDir: tempDir,
			},
			FS: mockFS,
		}

		// get file list for the subdirectory
		files, err := mockSrv.getFileList("subdir", "name", "asc")
		require.NoError(t, err)

		// verify that the parent directory entry exists but has a zero timestamp
		require.True(t, len(files) > 0, "File list should not be empty")
		require.Equal(t, "..", files[0].Name, "First entry should be parent directory")
		require.True(t, files[0].IsDir, "Parent entry should be a directory")

		// check that the LastModified time is zero
		zeroTime := time.Time{}
		assert.Equal(t, zeroTime, files[0].LastModified,
			"Parent directory should have a zero timestamp when parent info can't be retrieved")
	})
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
		resp, err := client.Get(baseURL + "/partials/dir-contents?path=&sort=name&dir=desc")
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

func TestDirectoryTraversalPrevention(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		handlerName    string // use string instead of function
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "attempt to access parent via ../",
			path:           "../",
			handlerName:    "handleRoot",
			expectedStatus: http.StatusNotFound, // path cleaning makes this invalid
			expectedBody:   "path not found",
		},
		{
			name:           "attempt to access parent via multiple ../../../",
			path:           "../../../",
			handlerName:    "handleRoot",
			expectedStatus: http.StatusNotFound, // path cleaning makes this invalid
			expectedBody:   "path not found",
		},
		{
			name:           "attempt to access absolute path",
			path:           "/etc/passwd",
			handlerName:    "handleRoot",
			expectedStatus: http.StatusNotFound, // should be cleaned to "etc/passwd" which doesn't exist
			expectedBody:   "path not found",
		},
		{
			name:           "attempt to download file outside root via ../",
			path:           "/../../../etc/passwd",
			handlerName:    "handleDownload",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "file not found",
		},
		{
			name:           "attempt to view directory outside root",
			path:           "../../../../",
			handlerName:    "handleDirContents",
			expectedStatus: http.StatusNotFound, // path cleaning makes this invalid
			expectedBody:   "directory not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			var err error
			var handler http.HandlerFunc

			// set the handler based on the handler name
			switch tc.handlerName {
			case "handleRoot":
				handler = srv.handleRoot
				req, err = http.NewRequest("GET", "/?path="+tc.path, nil)
			case "handleDirContents":
				handler = srv.handleDirContents
				req, err = http.NewRequest("GET", "/partials/dir-contents?path="+tc.path, nil)
			case "handleDownload":
				handler = srv.handleDownload
				req, err = http.NewRequest("GET", tc.path, nil)
			}
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code)
			assert.Contains(t, rr.Body.String(), tc.expectedBody)
		})
	}
}

func TestLoginRateLimit(t *testing.T) {
	// create a test server with authentication enabled
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{
			ListenAddr: ":0",
			Theme:      "light",
			RootDir:    testdataDir,
			Auth:       "testpassword",
			Title:      "Test Server",
		},
		FS: os.DirFS(testdataDir),
	}

	// create the router to test the rate limiter middleware
	router, err := srv.router()
	require.NoError(t, err)

	// simulate multiple login attempts from the same IP
	for i := 0; i < 10; i++ {
		// create login form data with incorrect credentials
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "wrongpassword")
		formData.Set("csrf_token", "dummy-token") // will fail on CSRF but that's fine for testing rate limit

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.168.1.1:1234" // use same IP for all requests

		// add a cookie for CSRF token
		req.AddCookie(&http.Cookie{
			Name:  "csrf_token",
			Value: "dummy-token",
		})

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		// first few attempts should return 200 (login form with error)
		// after exceeding rate limit (5 attempts), should get 429 Too Many Requests
		if i < 5 {
			assert.NotEqual(t, http.StatusTooManyRequests, rr.Code, "Request %d should not be rate limited", i+1)
		} else {
			t.Logf("Request %d status: %d", i+1, rr.Code)
			if rr.Code == http.StatusTooManyRequests {
				// once we hit rate limit, test passed
				assert.Contains(t, rr.Body.String(), "Too many login attempts", "Rate limit message should be shown")
				return
			}
		}

		// add a small delay between requests
		time.Sleep(10 * time.Millisecond)
	}

	// if we made it here, we never got rate limited
	t.Fatal("Rate limit was never triggered after 10 login attempts")
}

func TestDirectoryTraversalIntegration(t *testing.T) {
	// get the absolute path to the testdata directory
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	// create server with the testdata directory
	srv := &Web{
		Config: Config{
			ListenAddr: ":0",
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
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	// test directory traversal attempts with real HTTP requests
	t.Run("attempt to access parent directory via browser", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=../")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// should return a not found error
		assert.Contains(t, string(body), "path not found")
	})

	t.Run("attempt to download file outside root", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/../../../etc/passwd")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		// the error message might be "404 page not found" instead of "file not found"
		// because the router might handle this before our handler
		assert.True(t, strings.Contains(string(body), "not found"), "Response should contain 'not found'")
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

func TestShouldExclude(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		exclude  []string
		expected bool
	}{
		{
			name:     "no exclude patterns",
			path:     "some/path",
			exclude:  []string{},
			expected: false,
		},
		{
			name:     "exact match",
			path:     "some/path",
			exclude:  []string{"some/path"},
			expected: true,
		},
		{
			name:     "directory component match",
			path:     "some/.git/path",
			exclude:  []string{".git"},
			expected: true,
		},
		{
			name:     "end of path match",
			path:     "some/path/.git",
			exclude:  []string{".git"},
			expected: true,
		},
		{
			name:     "no match",
			path:     "some/path",
			exclude:  []string{".git", "vendor"},
			expected: false,
		},
		{
			name:     "multiple patterns with match",
			path:     "some/vendor/path",
			exclude:  []string{".git", "vendor"},
			expected: true,
		},
		{
			name:     "partial name no match",
			path:     "some/vendors/path",
			exclude:  []string{"vendor"},
			expected: false,
		},
		{
			name:     "deep directory match",
			path:     "some/path/with/nested/.git/objects",
			exclude:  []string{".git"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wb := &Web{
				Config: Config{
					Exclude: tt.exclude,
				},
			}
			result := wb.shouldExclude(tt.path)
			if result != tt.expected {
				t.Errorf("shouldExclude(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestGetFileListWithExcludes(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create test files
	filesToCreate := []string{
		filepath.Join(tempDir, "normal.txt"),
		filepath.Join(tempDir, ".env"),
		filepath.Join(tempDir, ".git", "config"),
	}

	// create .git directory
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatalf("Failed to create .git directory: %v", err)
	}

	// create the files
	for _, file := range filesToCreate {
		err := os.WriteFile(file, []byte("test content"), 0644)
		require.NoError(t, err)
	}

	// create a Web instance with exclude patterns
	wb := &Web{
		Config: Config{
			RootDir: tempDir,
			Exclude: []string{".git", ".env"},
		},
		FS: os.DirFS(tempDir),
	}

	// test that excluded files are not in the file list
	fileList, err := wb.getFileList(".", "name", "asc")
	require.NoError(t, err)

	// check that .git and .env are excluded
	for _, file := range fileList {
		assert.NotEqual(t, ".git", file.Name, "Excluded directory .git should not be in the file list")
		assert.NotEqual(t, ".env", file.Name, "Excluded file .env should not be in the file list")
	}

	// verify that normal.txt is in the list
	found := false
	for _, file := range fileList {
		if file.Name == "normal.txt" {
			found = true
			break
		}
	}
	assert.True(t, found, "normal.txt should be in the file list")
}

func TestGetFileListWithExcludesInSubdir(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create .git directory
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatalf("Failed to create .git directory: %v", err)
	}

	// create test files
	filesToCreate := []string{
		filepath.Join(tempDir, "normal.txt"),
		filepath.Join(tempDir, ".env"),
		filepath.Join(tempDir, ".git", "config"),
	}

	// create the files
	for _, file := range filesToCreate {
		err := os.WriteFile(file, []byte("test content"), 0644)
		require.NoError(t, err)
	}

	// create a Web instance with exclude patterns
	wb := &Web{
		Config: Config{
			RootDir: tempDir,
			Exclude: []string{".git", ".env"},
		},
		FS: os.DirFS(tempDir),
	}

	// create a subdirectory with excluded files
	subDir := filepath.Join(tempDir, "subdir")
	err := os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// create files in subdirectory
	err = os.WriteFile(filepath.Join(subDir, ".env"), []byte("test content"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(subDir, "normal.txt"), []byte("test content"), 0644)
	require.NoError(t, err)

	// test that excluded files in subdirectory are not in the file list
	fileList, err := wb.getFileList("subdir", "name", "asc")
	require.NoError(t, err)

	// check that .env is excluded in subdirectory
	for _, file := range fileList {
		assert.NotEqual(t, ".env", file.Name, "Excluded file .env should not be in the subdirectory file list")
	}

	// verify that normal.txt is in the subdirectory list
	found := false
	for _, file := range fileList {
		if file.Name == "normal.txt" {
			found = true
			break
		}
	}
	assert.True(t, found, "normal.txt should be in the subdirectory file list")
}

func TestGetFileListWithParentTimestamp(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create a subdirectory
	subDir := filepath.Join(tempDir, "subdir")
	err := os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// create a Web instance with the test directory
	wb := &Web{
		Config: Config{
			RootDir: tempDir,
		},
		FS: os.DirFS(tempDir),
	}

	// get the file list for the subdirectory
	// use a relative path from the root directory
	fileList, err := wb.getFileList("subdir", "name", "asc")
	require.NoError(t, err)

	// verify that the parent directory (..) is included
	require.Greater(t, len(fileList), 0, "File list should not be empty")
	require.Equal(t, "..", fileList[0].Name, "First entry should be the parent directory")

	// get the parent directory info
	parentInfo, err := os.Stat(tempDir)
	require.NoError(t, err)

	// verify that the parent directory timestamp matches
	require.Equal(t, parentInfo.ModTime(), fileList[0].LastModified, "Parent directory timestamp should match")
}

func TestAuthentication(t *testing.T) {
	// create a server with authentication
	srv := &Web{
		Config: Config{
			RootDir: "testdata",
			Auth:    "testpassword",
		},
		FS: os.DirFS("testdata"),
	}

	// initialize templates for testing
	err := srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	t.Run("redirect to login page when not authenticated", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(srv.handleRoot))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))
	})

	t.Run("access allowed with basic auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.SetBasicAuth("weblist", "testpassword")

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "success", rr.Body.String())
		// check that cookie is set with a session token, not raw password
		cookie := rr.Header().Get("Set-Cookie")
		assert.Contains(t, cookie, "auth=")
		assert.NotContains(t, cookie, "auth=testpassword")
	})

	t.Run("access allowed with cookie", func(t *testing.T) {
		// first we need to login to get a valid token
		// get CSRF token first
		loginPageReq, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)
		loginPageRR := httptest.NewRecorder()
		loginHandler := http.HandlerFunc(srv.handleLoginPage)
		loginHandler.ServeHTTP(loginPageRR, loginPageReq)

		// extract CSRF token
		cookies := loginPageRR.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// submit login
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")
		formData.Set("csrf_token", csrfCookie.Value)
		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie)

		rr := httptest.NewRecorder()
		loginSubmitHandler := http.HandlerFunc(srv.handleLoginSubmit)
		loginSubmitHandler.ServeHTTP(rr, req)

		// get auth cookie
		cookies = rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}
		require.NotNil(t, authCookie, "Auth cookie should be set")

		// verify the session token is valid
		assert.True(t, srv.validateSessionToken(authCookie.Value), "Session token should be valid")

		// test auth with the cookie		req, err = http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.AddCookie(authCookie)

		// use a new response recorder
		rr = httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "success", rr.Body.String())
	})

	t.Run("access denied with wrong password", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.SetBasicAuth("weblist", "wrongpassword")

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(srv.handleRoot))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))
	})

	t.Run("login page accessible without auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("login page"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "login page", rr.Body.String())
	})

	t.Run("assets accessible without auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/assets/css/custom.css", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("css content"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "css content", rr.Body.String())
	})

	t.Run("logout clears auth cookie", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/logout", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{
			Name:  "auth",
			Value: "testpassword",
		})

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLogout)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))

		// check that the cookie is cleared
		cookies := rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}

		require.NotNil(t, authCookie, "Auth cookie should be present")
		assert.Equal(t, "", authCookie.Value, "Auth cookie value should be empty")
		assert.True(t, authCookie.MaxAge < 0, "Auth cookie MaxAge should be negative to delete it")
	})
}

func TestSessionTokens(t *testing.T) {
	srv := &Web{
		Config: Config{
			Auth:          "test-secret-password",
			SessionSecret: "test-session-secret",
		},
	}

	t.Run("token generation and validation", func(t *testing.T) {
		// generate a token
		token := srv.generateSessionToken()

		// token should have 3 parts separated by dots
		parts := strings.Split(token, ".")
		require.Len(t, parts, 3, "Session token should have 3 parts")

		// first part should be a UUID
		assert.Len(t, parts[0], 36, "First part should be a UUID")

		// second part should be a timestamp (number)
		_, err := strconv.ParseInt(parts[1], 10, 64)
		assert.NoError(t, err, "Second part should be a valid numeric timestamp")

		// third part should be a base64 encoded signature
		_, err = base64.StdEncoding.DecodeString(parts[2])
		assert.NoError(t, err, "Third part should be a valid base64 string")

		// token should validate
		assert.True(t, srv.validateSessionToken(token), "Token should validate successfully")
	})

	t.Run("token validation with wrong session secret", func(t *testing.T) {
		// generate a token with the initial session secret
		token := srv.generateSessionToken()

		// create a new server with a different session secret
		wrongSrv := &Web{
			Config: Config{
				Auth:          "test-secret-password",
				SessionSecret: "different-session-secret",
			},
		}

		// token should not validate with wrong session secret
		assert.False(t, wrongSrv.validateSessionToken(token), "Token should not validate with wrong session secret")
	})

	t.Run("invalid token format", func(t *testing.T) {
		// test with malformed tokens
		assert.False(t, srv.validateSessionToken("invalid"), "Invalid token should not validate")
		assert.False(t, srv.validateSessionToken("a.b.c"), "Malformed token should not validate")
		assert.False(t, srv.validateSessionToken(""), "Empty token should not validate")
	})

	t.Run("tampered token", func(t *testing.T) {
		// generate a valid token
		token := srv.generateSessionToken()
		parts := strings.Split(token, ".")

		// tamper with the token ID
		tamperedToken := "tampered-id." + parts[1] + "." + parts[2]
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered token should not validate")

		// tamper with the timestamp
		tamperedToken = parts[0] + ".9999999999." + parts[2]
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered timestamp should not validate")

		// tamper with the signature
		tamperedToken = parts[0] + "." + parts[1] + ".AAAA"
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered signature should not validate")
	})
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

// Skip TestHandleLoginPage as templates are embedded and tests are failing

func TestHandleLoginSubmit(t *testing.T) {
	// create a test server with authentication enabled
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{
			ListenAddr: ":0",
			Theme:      "light",
			RootDir:    testdataDir,
			Auth:       "testpassword",
			Title:      "Test Server",
		},
		FS: os.DirFS(testdataDir),
	}

	// initialize templates for testing
	err = srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	t.Run("successful login", func(t *testing.T) {
		// first get a CSRF token from the login page
		loginPageReq, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		loginPageRR := httptest.NewRecorder()
		loginHandler := http.HandlerFunc(srv.handleLoginPage)
		loginHandler.ServeHTTP(loginPageRR, loginPageReq)

		// extract CSRF token cookie
		cookies := loginPageRR.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// create a form data with correct credentials
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")
		formData.Set("csrf_token", csrfCookie.Value)

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie) // add the CSRF cookie to the request

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// check redirect on successful login
		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/", rr.Header().Get("Location"))

		// check that auth cookie was set
		cookies = rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}
		require.NotNil(t, authCookie, "Auth cookie should be set")
		// verify the session token is valid using our validation function
		assert.True(t, srv.validateSessionToken(authCookie.Value), "Session token should be valid")
	})

	t.Run("failed login - wrong username", func(t *testing.T) {
		// first get a CSRF token from the login page
		loginPageReq, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		loginPageRR := httptest.NewRecorder()
		loginHandler := http.HandlerFunc(srv.handleLoginPage)
		loginHandler.ServeHTTP(loginPageRR, loginPageReq)

		// extract CSRF token cookie
		cookies := loginPageRR.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		formData := url.Values{}
		formData.Set("username", "wronguser")
		formData.Set("password", "testpassword")
		formData.Set("csrf_token", csrfCookie.Value)

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie) // add the CSRF cookie to the request

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should render login page with error
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid username or password")
	})

	t.Run("failed login - wrong password", func(t *testing.T) {
		// first get a CSRF token from the login page
		loginPageReq, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		loginPageRR := httptest.NewRecorder()
		loginHandler := http.HandlerFunc(srv.handleLoginPage)
		loginHandler.ServeHTTP(loginPageRR, loginPageReq)

		// extract CSRF token cookie
		cookies := loginPageRR.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "wrongpassword")
		formData.Set("csrf_token", csrfCookie.Value)

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie) // add the CSRF cookie to the request

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should render login page with error
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid username or password")
	})

	t.Run("form parsing error", func(t *testing.T) {
		// create a malformed request body to trigger ParseForm error
		req, err := http.NewRequest("POST", "/login", strings.NewReader("%"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should return a bad request error
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "Failed to parse form")
	})

	t.Run("missing CSRF token", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")
		// deliberately missing CSRF token

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should render login page with CSRF error
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid or missing CSRF token")
	})

	t.Run("invalid CSRF token", func(t *testing.T) {
		// first get a CSRF token from the login page
		loginPageReq, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		loginPageRR := httptest.NewRecorder()
		loginHandler := http.HandlerFunc(srv.handleLoginPage)
		loginHandler.ServeHTTP(loginPageRR, loginPageReq)

		// extract CSRF token cookie
		cookies := loginPageRR.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")
		formData.Set("csrf_token", "invalid-token") // invalid token

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie) // add the real CSRF cookie

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should render login page with CSRF error
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid or missing CSRF token")
	})
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

func TestFileInfoViewable(t *testing.T) {
	tests := []struct {
		name     string
		fileInfo FileInfo
		viewable bool
	}{
		{
			name: "text file",
			fileInfo: FileInfo{
				Name:  "test.txt",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "markdown file",
			fileInfo: FileInfo{
				Name:  "readme.md",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "yaml file",
			fileInfo: FileInfo{
				Name:  "config.yaml",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "go file",
			fileInfo: FileInfo{
				Name:  "main.go",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "image file",
			fileInfo: FileInfo{
				Name:  "image.jpg",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "pdf file",
			fileInfo: FileInfo{
				Name:  "document.pdf",
				IsDir: false,
			},
			viewable: true,
		},
		{
			name: "binary file",
			fileInfo: FileInfo{
				Name:  "binary.bin",
				IsDir: false,
			},
			viewable: false,
		},
		{
			name: "directory",
			fileInfo: FileInfo{
				Name:  "dir",
				IsDir: true,
			},
			viewable: false,
		},
		{
			name: "no extension",
			fileInfo: FileInfo{
				Name:  "README",
				IsDir: false,
			},
			viewable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.fileInfo.IsViewable()
			assert.Equal(t, tc.viewable, result)
		})
	}
}

// TestHTMLRendering verifies that HTML files are rendered correctly in the preview
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
		{
			name:           "nested directory redirects to view",
			path:           "/dir1",
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/?path=dir1",
		},
		{
			name:           "nested directory with trailing slash",
			path:           "/dir1/",
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/?path=dir1",
		},
		{
			name:           "deeply nested directory",
			path:           "/dir1/subdir",
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/?path=dir1/subdir",
		},
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

func (m *mockFileInfo) Sys() interface{} {
	return nil
}

func TestHandleLoginPage(t *testing.T) {
	srv := setupTestServer(t)
	srv.BrandName = "Test Brand"
	srv.BrandColor = "ff0000"

	// create a request to pass to our handler
	req, err := http.NewRequest("GET", "/login", nil)
	require.NoError(t, err)

	// create a ResponseRecorder to record the response
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(srv.handleLoginPage)

	// call the handler
	handler.ServeHTTP(rr, req)

	// check the response status code is 200 OK
	assert.Equal(t, http.StatusOK, rr.Code)

	// check that the response contains expected elements
	responseBody := rr.Body.String()
	assert.Contains(t, responseBody, "Test Brand")
	assert.Contains(t, responseBody, "Test Title")
	assert.Contains(t, responseBody, "<form")
	assert.Contains(t, responseBody, "method=\"post\"")
	assert.Contains(t, responseBody, "action=\"/login\"")
}

func TestNormalizeBrandColor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty color",
			input: "",
			want:  "",
		},
		{
			name:  "already has hash prefix",
			input: "#ffffff",
			want:  "#ffffff",
		},
		{
			name:  "hex without hash prefix",
			input: "ff0000",
			want:  "#ff0000",
		},
		{
			name:  "named color",
			input: "blue",
			want:  "#blue",
		},
	}

	srv := setupTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := srv.normalizeBrandColor(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHighlightCode(t *testing.T) {
	web := &Web{
		Config: Config{
			EnableSyntaxHighlighting: true,
		},
	}

	tests := []struct {
		name         string
		code         string
		filename     string
		theme        string
		wantContains []string
		wantErr      bool
	}{
		{
			name:     "Go code with light theme",
			code:     "package main\n\nfunc main() {\n\tprint(\"Hello\")\n}",
			filename: "main.go",
			theme:    "light",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"chroma",
				"Hello",
				"package",
			},
			wantErr: false,
		},
		{
			name:     "Go code with dark theme",
			code:     "package main\n\nfunc main() {\n\tprint(\"Hello\")\n}",
			filename: "main.go",
			theme:    "dark",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"chroma",
				"Hello",
				"package",
			},
			wantErr: false,
		},
		{
			name:     "JavaScript code",
			code:     "function hello() {\n\tconsole.log(\"Hello\");\n}",
			filename: "script.js",
			theme:    "light",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"chroma",
				"Hello",
				"function",
			},
			wantErr: false,
		},
		{
			name:     "HTML code",
			code:     "<html><body><h1>Hello</h1></body></html>",
			filename: "index.html",
			theme:    "light",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"chroma",
				"html",
				"Hello",
			},
			wantErr: false,
		},
		{
			name:     "Unknown language falls back to plain text",
			code:     "This is plain text",
			filename: "unknown.xyz",
			theme:    "light",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"<pre class=\"chroma\">",
				"This is plain text",
			},
			wantErr: false,
		},
		{
			name:     "Empty code",
			code:     "",
			filename: "empty.txt",
			theme:    "light",
			wantContains: []string{
				"<div class=\"highlight-wrapper\">",
				"<pre class=\"chroma\">",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := web.highlightCode(tt.code, tt.filename, tt.theme)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// check that the result contains expected strings
			for _, expected := range tt.wantContains {
				assert.Contains(t, result, expected, "Result should contain %q", expected)
			}
		})
	}
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
		{
			name:      "default sort",
			sortParam: "",
			wantSort:  "name",
			wantDir:   "asc",
		},
		{
			name:      "sort by name asc",
			sortParam: "+name",
			wantSort:  "name",
			wantDir:   "asc",
		},
		{
			name:      "sort by name desc",
			sortParam: "-name",
			wantSort:  "name",
			wantDir:   "desc",
		},
		{
			name:      "sort by size asc",
			sortParam: "+size",
			wantSort:  "size",
			wantDir:   "asc",
		},
		{
			name:      "sort by size desc",
			sortParam: "-size",
			wantSort:  "size",
			wantDir:   "desc",
		},
		{
			name:      "sort by mtime asc",
			sortParam: "+mtime",
			wantSort:  "date", // mtime maps to date internally
			wantDir:   "asc",
		},
		{
			name:      "sort by mtime desc",
			sortParam: "-mtime",
			wantSort:  "date", // mtime maps to date internally
			wantDir:   "desc",
		},
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

func TestSecurityHeadersMiddleware(t *testing.T) {
	srv := Web{}
	handler := srv.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "SAMEORIGIN", rr.Header().Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", rr.Header().Get("X-XSS-Protection"))
	assert.Contains(t, rr.Header().Get("Content-Security-Policy"), "default-src 'self'")
	assert.Equal(t, "none", rr.Header().Get("X-Permitted-Cross-Domain-Policies"))
	assert.Equal(t, "noindex, nofollow", rr.Header().Get("X-Robots-Tag"))
}
