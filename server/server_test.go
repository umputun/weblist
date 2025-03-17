package server

import (
	"context"
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
	// Get the absolute path to the testdata directory
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	// Ensure the testdata directory exists
	_, err = os.Stat(testdataDir)
	require.NoError(t, err, "testdata directory must exist at %s", testdataDir)

	// Create server with the testdata directory
	srv := &Web{
		Config: Config{
			ListenAddr: ":8080",
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
			name:           "Root directory",
			path:           "",
			expectedStatus: http.StatusOK,
			expectedBody:   "file1.txt", // Should contain this file name
		},
		{
			name:           "Subdirectory",
			path:           "dir1",
			expectedStatus: http.StatusOK,
			expectedBody:   "file3.txt", // Should contain this file name
		},
		{
			name:           "Non-existent directory",
			path:           "non-existent",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "path not found",
		},
		{
			name:           "File path redirects to download",
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
			name:           "Download existing file",
			path:           "/download/file1.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file1.txt\"",
			},
			expectedBody: "file1 content",
		},
		{
			name:           "Download file in subdirectory",
			path:           "/download/dir1/file3.txt",
			expectedStatus: http.StatusOK,
			expectedHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment; filename=\"file3.txt\"",
			},
			expectedBody: "file3 content",
		},
		{
			name:           "Download non-existent file",
			path:           "/download/non-existent.txt",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "file not found",
		},
		{
			name:           "Cannot download directory",
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
			name:           "Root directory default sort",
			path:           "",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name:           "Root directory sort by name desc",
			path:           "",
			sort:           "name",
			dir:            "desc",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file1.txt", "file2.txt", "dir1", "dir2"},
		},
		{
			name:           "Subdirectory",
			path:           "dir1",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusOK,
			expectedBody:   []string{"file3.txt", "subdir"},
		},
		{
			name:           "Non-existent directory",
			path:           "non-existent",
			sort:           "",
			dir:            "",
			expectedStatus: http.StatusNotFound,
			expectedBody:   []string{"directory not found"},
		},
		{
			name:           "File path is not a directory",
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
			name:           "Root directory",
			path:           ".",
			sortBy:         "name",
			sortDir:        "asc",
			expectedFiles:  []string{"file1.txt", "file2.txt"},
			expectedDirs:   []string{"dir1", "dir2", "empty-dir"},
			expectedParent: false,
		},
		{
			name:           "Subdirectory",
			path:           "dir1",
			sortBy:         "name",
			sortDir:        "asc",
			expectedFiles:  []string{"file3.txt"},
			expectedDirs:   []string{"subdir"},
			expectedParent: true,
		},
		{
			name:           "Sort by name descending",
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

			// Check if parent directory is included when expected
			if tc.expectedParent {
				assert.True(t, len(files) > 0 && files[0].Name == "..")
			}

			// Check if all expected files are present
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

			// Check files
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

			// Check directories
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
			name:          "Root path",
			path:          ".",
			sortBy:        "name",
			sortDir:       "asc",
			expectedParts: []map[string]string{},
		},
		{
			name:    "Single level path",
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
			name:    "Multi level path",
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

func TestSortFiles(t *testing.T) {
	srv := setupTestServer(t)

	// Create test files with different attributes
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
			name:          "Sort by name ascending",
			sortBy:        "name",
			sortDir:       "asc",
			expectedOrder: []string{"..", "dir1", "dir2", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:          "Sort by name descending",
			sortBy:        "name",
			sortDir:       "desc",
			expectedOrder: []string{"..", "dir1", "dir2", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:          "Sort by size ascending",
			sortBy:        "size",
			sortDir:       "asc",
			expectedOrder: []string{"..", "dir1", "dir2", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:          "Sort by size descending",
			sortBy:        "size",
			sortDir:       "desc",
			expectedOrder: []string{"..", "dir1", "dir2", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:          "Sort by date ascending",
			sortBy:        "date",
			sortDir:       "asc",
			expectedOrder: []string{"..", "dir1", "dir2", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:          "Sort by date descending",
			sortBy:        "date",
			sortDir:       "desc",
			expectedOrder: []string{"..", "dir1", "dir2", "c.txt", "b.txt", "a.txt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Make a copy of the files slice to avoid modifying the original
			filesCopy := make([]FileInfo, len(files))
			copy(filesCopy, files)

			// Sort the files
			srv.sortFiles(filesCopy, tc.sortBy, tc.sortDir)

			// Check if the order matches expected
			assert.Equal(t, len(tc.expectedOrder), len(filesCopy))
			for i, expectedName := range tc.expectedOrder {
				if i < len(filesCopy) {
					assert.Equal(t, expectedName, filesCopy[i].Name)
				}
			}
		})
	}
}

func TestRun(t *testing.T) {
	srv := setupTestServer(t)

	// Create a context that will be canceled after a short time
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run the server in a goroutine
	errCh := make(chan error)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	// Wait for the context to be canceled or for an error
	select {
	case err := <-errCh:
		// We expect nil error when the context is canceled
		assert.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Server did not shut down within expected time")
	}
}
