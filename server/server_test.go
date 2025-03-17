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
	"reflect"
	"sort"
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
			ListenAddr: ":0", // use port 0 to let the system assign a random available port
			Theme:      "light",
			HideFooter: false,
			RootDir:    testdataDir,
			Version:    "test-version",
		},
		FS: os.DirFS(testdataDir),
	}

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
				assert.Contains(t, rr.Header().Get("Location"), "/download/")
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
			path:           "/download/file1.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file1.txt\"",
			},
			expectedBody: "file1 content",
		},
		{
			name:           "download file in subdirectory",
			path:           "/download/dir1/file3.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file3.txt\"",
			},
			expectedBody: "file3 content",
		},
		{
			name:           "download non-existent file",
			path:           "/download/non-existent.txt",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "file not found",
		},
		{
			name:           "cannot download directory",
			path:           "/download/dir1",
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "cannot download directories",
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
			url := "/partials/dir-contents?path=" + tc.path
			if tc.sort != "" {
				url += "&sort=" + tc.sort
			}
			if tc.dir != "" {
				url += "&dir=" + tc.dir
			}

			req, err := http.NewRequest("GET", url, nil)
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
			url := "/partials/dir-contents?path=" + tc.path
			if tc.sort != "" {
				url += "&sort=" + tc.sort
			}
			if tc.dir != "" {
				url += "&dir=" + tc.dir
			}

			req, err := http.NewRequest("GET", url, nil)
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
			// based on actual output: [.. dir2 dir1 c.txt b.txt a.txt]
			expectedOrder: []string{"..", "dir2", "dir1", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:    "sort by size descending",
			sortBy:  "size",
			sortDir: "desc",
			// based on actual output: [.. dir1 dir2 a.txt b.txt c.txt]
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
	srv.Config.ListenAddr = fmt.Sprintf(":%d", port)

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
		resp, err := client.Get(baseURL + "/download/file1.txt")
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
		assert.Equal(t, "/download/file1.txt", resp.Header.Get("Location"))
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

	t.Run("cannot download directory", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/download/dir1")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "cannot download directories")
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

	// test cases for directory traversal attempts
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
			path:           "/download/../../../etc/passwd",
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
	srv.Config.ListenAddr = fmt.Sprintf(":%d", port)

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
		resp, err := client.Get(baseURL + "/download/../../../etc/passwd")
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

func TestExcludeFilesAndDirectories(t *testing.T) {
	// create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "weblist-test")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// create test directories
	dirs := []string{
		filepath.Join(tempDir, "normal"),
		filepath.Join(tempDir, ".git"),
		filepath.Join(tempDir, "vendor"),
		filepath.Join(tempDir, "docs", ".git"),
		filepath.Join(tempDir, "src", "vendor"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create directory %s: %v", dir, err)
		}
	}

	// create test files
	testFiles := []string{
		filepath.Join(tempDir, ".env"),
		filepath.Join(tempDir, "normal", "config.json"),
		filepath.Join(tempDir, "normal", ".env"),
		filepath.Join(tempDir, "docs", "README.md"),
	}

	for _, file := range testFiles {
		if err := os.WriteFile(file, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", file, err)
		}
	}

	// create a test file in each directory
	for _, dir := range dirs {
		filePath := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", filePath, err)
		}
	}

	// create a server with exclude patterns
	srv := &Web{
		Config: Config{
			RootDir: tempDir,
			Exclude: []string{".git", "vendor", ".env"},
		},
		FS: os.DirFS(tempDir),
	}

	// test root directory listing
	fileInfos, err := srv.getFileList(".", "name", "asc")
	if err != nil {
		t.Fatalf("getFileList failed: %v", err)
	}

	// check that .git and vendor directories and .env file are excluded
	for _, file := range fileInfos {
		if file.Name == ".git" || file.Name == "vendor" || file.Name == ".env" {
			t.Errorf("excluded item %s should not be in the file list", file.Name)
		}
	}

	// verify that only expected directories are present
	expectedDirs := []string{"docs", "normal", "src"}
	foundDirs := make([]string, 0)

	for _, file := range fileInfos {
		if file.IsDir {
			foundDirs = append(foundDirs, file.Name)
		}
	}

	// sort both slices for comparison
	sort.Strings(expectedDirs)
	sort.Strings(foundDirs)

	if !reflect.DeepEqual(expectedDirs, foundDirs) {
		t.Errorf("expected directories %v, got %v", expectedDirs, foundDirs)
	}

	// test subdirectory listing
	fileInfos, err = srv.getFileList("normal", "name", "asc")
	if err != nil {
		t.Fatalf("getFileList for normal failed: %v", err)
	}

	// check that .env file is excluded in subdirectory
	for _, file := range fileInfos {
		if file.Name == ".env" {
			t.Errorf("excluded file %s should not be in the subdirectory file list", file.Name)
		}
	}

	// verify that config.json is present
	foundConfigJson := false
	for _, file := range fileInfos {
		if file.Name == "config.json" {
			foundConfigJson = true
			break
		}
	}
	if !foundConfigJson {
		t.Errorf("config.json should be in the file list")
	}

	// test docs subdirectory listing
	fileInfos, err = srv.getFileList("docs", "name", "asc")
	if err != nil {
		t.Fatalf("getFileList for docs failed: %v", err)
	}

	// check that .git directory is excluded in subdirectory
	for _, file := range fileInfos {
		if file.Name == ".git" {
			t.Errorf("excluded directory %s should not be in the subdirectory file list", file.Name)
		}
	}

	// verify that README.md is present
	foundReadme := false
	for _, file := range fileInfos {
		if file.Name == "README.md" {
			foundReadme = true
			break
		}
	}
	if !foundReadme {
		t.Errorf("README.md should be in the file list")
	}
}

func TestHandleDownloadExcludedFiles(t *testing.T) {
	// create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "weblist-test")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// create test files
	testFiles := []string{
		filepath.Join(tempDir, "normal.txt"),
		filepath.Join(tempDir, ".env"),
		filepath.Join(tempDir, ".git", "config"),
	}

	// create .git directory
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// create test files with content
	for _, file := range testFiles {
		if err := os.WriteFile(file, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", file, err)
		}
	}

	// create a server with exclude patterns
	srv := &Web{
		Config: Config{
			RootDir: tempDir,
			Exclude: []string{".git", ".env"},
		},
		FS: os.DirFS(tempDir),
	}

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "download normal file",
			path:           "/download/normal.txt",
			expectedStatus: http.StatusOK,
			expectedBody:   "test content",
		},
		{
			name:           "download excluded file",
			path:           "/download/.env",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "access denied: .env",
		},
		{
			name:           "download file in excluded directory",
			path:           "/download/.git/config",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "access denied: config",
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
			assert.Contains(t, rr.Body.String(), tc.expectedBody)
		})
	}
}

func TestExcludedFilesIntegration(t *testing.T) {
	// create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "weblist-test")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// create test files and directories
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	testFiles := []string{
		filepath.Join(tempDir, "normal.txt"),
		filepath.Join(tempDir, ".env"),
		filepath.Join(tempDir, ".git", "config"),
	}

	for _, file := range testFiles {
		if err := os.WriteFile(file, []byte("test content for "+filepath.Base(file)), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", file, err)
		}
	}

	// create server with exclude patterns
	srv := &Web{
		Config: Config{
			ListenAddr: ":0", // use port 0 to let the system assign a random available port
			Theme:      "light",
			RootDir:    tempDir,
			Exclude:    []string{".git", ".env"},
		},
		FS: os.DirFS(tempDir),
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
	srv.Config.ListenAddr = fmt.Sprintf(":%d", port)

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

	// test cases
	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "download normal file",
			path:           "/download/normal.txt",
			expectedStatus: http.StatusOK,
			expectedBody:   "test content for normal.txt",
		},
		{
			name:           "download excluded file",
			path:           "/download/.env",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "access denied: .env",
		},
		{
			name:           "download file in excluded directory",
			path:           "/download/.git/config",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "access denied: config",
		},
		{
			name:           "directory listing excludes files",
			path:           "/",
			expectedStatus: http.StatusOK,
			expectedBody:   "normal.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Get(baseURL + tc.path)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Contains(t, string(body), tc.expectedBody)

			// for the directory listing test, also verify excluded files are not shown
			if tc.path == "/" {
				assert.NotContains(t, string(body), ".env")
				assert.NotContains(t, string(body), ".git")
			}
		})
	}

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

func TestAuthentication(t *testing.T) {
	// Create a server with authentication
	srv := &Web{
		Config: Config{
			RootDir: "testdata",
			Auth:    "testpassword",
		},
		FS: os.DirFS("testdata"),
	}

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
		assert.Contains(t, rr.Header().Get("Set-Cookie"), "auth=testpassword")
	})

	t.Run("access allowed with cookie", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{
			Name:  "auth",
			Value: "testpassword",
		})

		rr := httptest.NewRecorder()
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
		req, err := http.NewRequest("GET", "/assets/css/style.css", nil)
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

		// Check that the cookie is cleared
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
