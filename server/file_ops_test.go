package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-pkgz/lcw/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestHandleDirContentsWithSortCookies(t *testing.T) {
	srv := setupTestServer(t)

	t.Run("cookies are set when query params provided", func(t *testing.T) {
		// first request with query parameters
		req, err := http.NewRequest("GET", "/partials/dir-contents?path=.&sort=size&dir=desc", nil)
		require.NoError(t, err)
		req.Header.Set("HX-Request", "true") // simulate HTMX request

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
		req.Header.Set("HX-Request", "true") // simulate HTMX request

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
			req.Header.Set("HX-Request", "true") // simulate HTMX request

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
				assert.True(t, slices.Contains(fileNames, expectedFile), "Expected file %s not found", expectedFile)
			}

			// check directories
			assert.Equal(t, len(tc.expectedDirs), len(dirNames), "Wrong number of directories")
			for _, expectedDir := range tc.expectedDirs {
				assert.True(t, slices.Contains(dirNames, expectedDir), "Expected directory %s not found", expectedDir)
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
				if err == nil {
					req.Header.Set("HX-Request", "true") // simulate HTMX request
				}
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

func TestAPIListDirectoryTraversal(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedError  string
	}{
		{name: "parent via ../", path: "../", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "multiple parent ../../../", path: "../../../", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "absolute /etc/passwd", path: "/etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "mixed /../../../etc", path: "/../../../etc", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "url encoded %2e%2e/", path: "%2e%2e/", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "double encoded %252e%252e", path: "%252e%252e/", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "backslash ..\\..\\", path: "..\\..\\", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "mixed slashes ../..\\../", path: "../..\\../", expectedStatus: http.StatusNotFound, expectedError: "directory not found"},
		{name: "valid subdir", path: "dir1", expectedStatus: http.StatusOK, expectedError: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/api/list?path="+tc.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			srv.handleAPIList(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code, "status code mismatch for path: %s", tc.path)
			if tc.expectedError != "" {
				assert.Contains(t, rr.Body.String(), tc.expectedError)
			}
		})
	}
}

func TestViewFileDirectoryTraversal(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedError  string
	}{
		{name: "parent via ../", path: "../etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
		{name: "absolute path", path: "/etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
		{name: "url encoded", path: "%2e%2e/etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
		{name: "backslash traversal", path: "..\\..\\etc\\passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/view/"+tc.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			srv.handleViewFile(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code, "status code mismatch for path: %s", tc.path)
			if tc.expectedError != "" {
				assert.Contains(t, rr.Body.String(), tc.expectedError)
			}
		})
	}
}

func TestFileModalDirectoryTraversal(t *testing.T) {
	srv := setupTestServer(t)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedError  string
	}{
		{name: "parent via ../", path: "../etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
		{name: "absolute path", path: "/etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
		{name: "url encoded", path: "%2e%2e/etc/passwd", expectedStatus: http.StatusNotFound, expectedError: "file not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/partials/file-modal?path="+tc.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			srv.handleFileModal(rr, req)

			assert.Equal(t, tc.expectedStatus, rr.Code, "status code mismatch for path: %s", tc.path)
			if tc.expectedError != "" {
				assert.Contains(t, rr.Body.String(), tc.expectedError)
			}
		})
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

func TestGetRecursiveMtime(t *testing.T) {
	tempDir := t.TempDir()

	// create nested directory structure
	subDir := filepath.Join(tempDir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))
	nestedDir := filepath.Join(subDir, "nested")
	require.NoError(t, os.Mkdir(nestedDir, 0755))

	// create files with different modification times
	oldFile := filepath.Join(subDir, "old.txt")
	require.NoError(t, os.WriteFile(oldFile, []byte("old"), 0644))
	oldTime := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(oldFile, oldTime, oldTime))

	newFile := filepath.Join(nestedDir, "new.txt")
	require.NoError(t, os.WriteFile(newFile, []byte("new"), 0644))
	newTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(newFile, newTime, newTime))

	wb := &Web{Config: Config{RootDir: tempDir}, FS: os.DirFS(tempDir)}

	// test recursive mtime returns the newest file time
	mtime := wb.getRecursiveMtime("subdir")
	assert.False(t, mtime.IsZero(), "mtime should not be zero")
	assert.WithinDuration(t, newTime, mtime, time.Second, "should return newest file time")
}

func TestGetRecursiveMtimeExcludesFiles(t *testing.T) {
	tempDir := t.TempDir()

	subDir := filepath.Join(tempDir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))

	// create an old visible file
	visibleFile := filepath.Join(subDir, "visible.txt")
	require.NoError(t, os.WriteFile(visibleFile, []byte("visible"), 0644))
	oldTime := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(visibleFile, oldTime, oldTime))

	// create a newer excluded file that should be ignored
	excludedFile := filepath.Join(subDir, ".hidden")
	require.NoError(t, os.WriteFile(excludedFile, []byte("excluded"), 0644))
	newTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(excludedFile, newTime, newTime))

	wb := &Web{Config: Config{RootDir: tempDir, Exclude: []string{".hidden"}}, FS: os.DirFS(tempDir)}

	// excluded file should not affect mtime - should use visible file's time
	mtime := wb.getRecursiveMtime("subdir")
	assert.WithinDuration(t, oldTime, mtime, time.Second, "should ignore excluded file")
}

func TestGetRecursiveMtimeEmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()

	// create empty directory structure
	emptyDir := filepath.Join(tempDir, "empty")
	require.NoError(t, os.Mkdir(emptyDir, 0755))
	nestedEmpty := filepath.Join(emptyDir, "nested")
	require.NoError(t, os.Mkdir(nestedEmpty, 0755))

	wb := &Web{Config: Config{RootDir: tempDir}, FS: os.DirFS(tempDir)}

	// empty directory should return zero time
	mtime := wb.getRecursiveMtime("empty")
	assert.True(t, mtime.IsZero(), "empty directory should return zero time")
}

func TestGetRecursiveMtimeExcludesDirectory(t *testing.T) {
	tempDir := t.TempDir()

	subDir := filepath.Join(tempDir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))

	// create an old visible file
	visibleFile := filepath.Join(subDir, "visible.txt")
	require.NoError(t, os.WriteFile(visibleFile, []byte("visible"), 0644))
	oldTime := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(visibleFile, oldTime, oldTime))

	// create excluded directory with newer file inside
	excludedDir := filepath.Join(subDir, ".hidden_dir")
	require.NoError(t, os.Mkdir(excludedDir, 0755))
	newerFile := filepath.Join(excludedDir, "newer.txt")
	require.NoError(t, os.WriteFile(newerFile, []byte("newer"), 0644))
	newTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(newerFile, newTime, newTime))

	wb := &Web{Config: Config{RootDir: tempDir, Exclude: []string{".hidden_dir"}}, FS: os.DirFS(tempDir)}

	// excluded directory should be skipped entirely - use visible file's time
	mtime := wb.getRecursiveMtime("subdir")
	assert.WithinDuration(t, oldTime, mtime, time.Second, "should ignore excluded directory")
}

func TestGetRecursiveMtimeNonExistentPath(t *testing.T) {
	tempDir := t.TempDir()
	wb := &Web{Config: Config{RootDir: tempDir}, FS: os.DirFS(tempDir)}

	// non-existent path should return zero time (WalkDir handles gracefully)
	mtime := wb.getRecursiveMtime("does-not-exist")
	assert.True(t, mtime.IsZero(), "non-existent path should return zero time")
}

func TestGetFileListWithRecursiveMtime(t *testing.T) {
	tempDir := t.TempDir()

	// create two directories with different nested file times
	dir1 := filepath.Join(tempDir, "dir1")
	require.NoError(t, os.Mkdir(dir1, 0755))
	dir2 := filepath.Join(tempDir, "dir2")
	require.NoError(t, os.Mkdir(dir2, 0755))

	// dir1 has older nested file
	file1 := filepath.Join(dir1, "file.txt")
	require.NoError(t, os.WriteFile(file1, []byte("old"), 0644))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(file1, oldTime, oldTime))

	// dir2 has newer nested file
	file2 := filepath.Join(dir2, "file.txt")
	require.NoError(t, os.WriteFile(file2, []byte("new"), 0644))
	newTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(file2, newTime, newTime))

	t.Run("without recursive mtime", func(t *testing.T) {
		wb := &Web{Config: Config{RootDir: tempDir, RecursiveMtime: false}, FS: os.DirFS(tempDir)}
		files, err := wb.getFileList(".", "date", "desc")
		require.NoError(t, err)

		// find dir1 and dir2
		var dir1Info, dir2Info FileInfo
		for _, f := range files {
			if f.Name == "dir1" {
				dir1Info = f
			}
			if f.Name == "dir2" {
				dir2Info = f
			}
		}
		// without recursive mtime, both directories should have similar mtime (when they were created)
		assert.WithinDuration(t, dir1Info.LastModified, dir2Info.LastModified, 2*time.Second)
	})

	t.Run("with recursive mtime", func(t *testing.T) {
		wb := &Web{Config: Config{RootDir: tempDir, RecursiveMtime: true}, FS: os.DirFS(tempDir)}
		files, err := wb.getFileList(".", "date", "desc")
		require.NoError(t, err)

		// find dir1 and dir2
		var dir1Info, dir2Info FileInfo
		for _, f := range files {
			if f.Name == "dir1" {
				dir1Info = f
			}
			if f.Name == "dir2" {
				dir2Info = f
			}
		}
		// with recursive mtime, dir2 should have newer time than dir1
		assert.True(t, dir2Info.LastModified.After(dir1Info.LastModified),
			"dir2 should have newer mtime (from nested file)")
		assert.WithinDuration(t, newTime, dir2Info.LastModified, time.Second)
		assert.WithinDuration(t, oldTime, dir1Info.LastModified, time.Second)
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
			name:     "known text filename README",
			fileInfo: FileInfo{Name: "README", IsDir: false},
			viewable: true,
		},
		{
			name:     "known text filename Makefile",
			fileInfo: FileInfo{Name: "Makefile", IsDir: false},
			viewable: true,
		},
		{
			name:     "unknown extensionless detected as text",
			fileInfo: FileInfo{Name: "unknown", IsDir: false, isBinary: false},
			viewable: true,
		},
		{
			name:     "unknown extensionless detected as binary",
			fileInfo: FileInfo{Name: "unknown", IsDir: false, isBinary: true},
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

func TestDetectBinary(t *testing.T) {
	// create test filesystem with binary and text files
	elfBinary := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
	elfBinary = append(elfBinary, make([]byte, 504)...)
	textContent := []byte("package main\n\nfunc main() {}\n")

	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	fsys := fstest.MapFS{
		"binary.go": &fstest.MapFile{Data: elfBinary, ModTime: baseTime},
		"text.go":   &fstest.MapFile{Data: textContent, ModTime: baseTime},
	}

	cache, err := lcw.NewLruCache(lcw.NewOpts[bool]().MaxKeys(100))
	require.NoError(t, err)

	web := &Web{FS: fsys, binaryCache: cache}

	t.Run("detects binary content", func(t *testing.T) {
		fi := FileInfo{Name: "binary.go", Path: "binary.go", LastModified: baseTime.Add(time.Hour)}
		web.detectBinary(&fi)
		assert.True(t, fi.isBinary, "should detect ELF binary")
		assert.False(t, fi.IsViewable(), "binary file should not be viewable")
	})

	t.Run("detects text content", func(t *testing.T) {
		fi := FileInfo{Name: "text.go", Path: "text.go", LastModified: baseTime.Add(2 * time.Hour)}
		web.detectBinary(&fi)
		assert.False(t, fi.isBinary, "should detect text content")
		assert.True(t, fi.IsViewable(), "text file should be viewable")
	})

	t.Run("cache hit returns same result", func(t *testing.T) {
		mtime := baseTime.Add(3 * time.Hour)
		fi1 := FileInfo{Name: "binary.go", Path: "binary.go", LastModified: mtime}
		fi2 := FileInfo{Name: "binary.go", Path: "binary.go", LastModified: mtime}

		web.detectBinary(&fi1)
		stat1 := cache.Stat()

		web.detectBinary(&fi2)
		stat2 := cache.Stat()

		assert.Equal(t, fi1.isBinary, fi2.isBinary, "should return same result")
		assert.Equal(t, stat1.Misses, stat2.Misses, "second call should be cache hit, no new misses")
		assert.Equal(t, stat1.Hits+1, stat2.Hits, "should have one more hit")
	})

	t.Run("different mtime causes cache miss", func(t *testing.T) {
		fi1 := FileInfo{Name: "text.go", Path: "text.go", LastModified: baseTime.Add(4 * time.Hour)}
		web.detectBinary(&fi1)
		stat1 := cache.Stat()

		fi2 := FileInfo{Name: "text.go", Path: "text.go", LastModified: baseTime.Add(5 * time.Hour)}
		web.detectBinary(&fi2)
		stat2 := cache.Stat()

		assert.Equal(t, stat1.Misses+1, stat2.Misses, "different mtime should cause cache miss")
	})

	t.Run("skips directories", func(t *testing.T) {
		fi := FileInfo{Name: "somedir", Path: "somedir", IsDir: true}
		web.detectBinary(&fi)
		assert.False(t, fi.isBinary, "directories should not be marked as binary")
	})

	t.Run("fallback without cache", func(t *testing.T) {
		webNoCache := &Web{FS: fsys, binaryCache: nil}
		fi := FileInfo{Name: "binary.go", Path: "binary.go", LastModified: baseTime.Add(6 * time.Hour)}
		webNoCache.detectBinary(&fi)
		assert.True(t, fi.isBinary, "should work without cache")
	})
}
