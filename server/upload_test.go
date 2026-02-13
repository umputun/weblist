package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMultipartRequest builds a multipart/form-data request with given files and form fields
func createMultipartRequest(t *testing.T, files, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// add form fields
	for k, v := range fields {
		require.NoError(t, writer.WriteField(k, v))
	}

	// add files
	for name, content := range files {
		part, err := writer.CreateFormFile("file", name)
		require.NoError(t, err)
		_, err = io.WriteString(part, content)
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestHandleUpload_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	req := createMultipartRequest(t, map[string]string{"test.txt": "hello world"}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp uploadResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, []string{"test.txt"}, resp.Uploaded)
	assert.Empty(t, resp.Error)

	// verify file was written
	content, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(content))
}

func TestHandleUpload_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	files := map[string]string{"a.txt": "content a", "b.txt": "content b", "c.txt": "content c"}
	req := createMultipartRequest(t, files, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp uploadResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Len(t, resp.Uploaded, 3)

	// verify all files exist
	for name, expected := range files {
		content, err := os.ReadFile(filepath.Join(tmpDir, name))
		require.NoError(t, err)
		assert.Equal(t, expected, string(content))
	}
}

func TestHandleUpload_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	tests := []struct {
		name string
		path string
	}{
		{"dot-dot path", "../../etc"},
		{"dot-dot filename style", "../secrets"},
		{"absolute path", "/etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createMultipartRequest(t, map[string]string{"test.txt": "data"}, map[string]string{"path": tt.path})
			rr := httptest.NewRecorder()
			srv.handleUpload(rr, req)
			assert.Equal(t, http.StatusBadRequest, rr.Code, "path=%q should be rejected", tt.path)
		})
	}
}

func TestHandleUpload_DuplicateFileRejected(t *testing.T) {
	tmpDir := t.TempDir()
	// create an existing file
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "existing.txt"), []byte("original"), 0o644))

	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20, UploadOverwrite: false}}

	req := createMultipartRequest(t, map[string]string{"existing.txt": "new content"}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)

	// verify original file is unchanged
	content, err := os.ReadFile(filepath.Join(tmpDir, "existing.txt"))
	require.NoError(t, err)
	assert.Equal(t, "original", string(content))
}

func TestHandleUpload_OverwriteEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "existing.txt"), []byte("original"), 0o644))

	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20, UploadOverwrite: true}}

	req := createMultipartRequest(t, map[string]string{"existing.txt": "new content"}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// verify file was overwritten
	content, err := os.ReadFile(filepath.Join(tmpDir, "existing.txt"))
	require.NoError(t, err)
	assert.Equal(t, "new content", string(content))
}

func TestHandleUpload_OverwriteRejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// create a target file outside the root
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("sensitive data"), 0o644))

	// create a symlink inside root pointing to the outside file
	symlinkPath := filepath.Join(tmpDir, "link.txt")
	require.NoError(t, os.Symlink(outsideFile, symlinkPath))

	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20, UploadOverwrite: true}}

	req := createMultipartRequest(t, map[string]string{"link.txt": "malicious content"}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	// should reject the upload
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "symlink")

	// verify the outside file was NOT overwritten
	content, err := os.ReadFile(outsideFile)
	require.NoError(t, err)
	assert.Equal(t, "sensitive data", string(content))
}

func TestHandleUpload_OversizedFile(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 100}} // 100 bytes limit

	// create a file larger than the limit
	largeContent := strings.Repeat("x", 200)
	req := createMultipartRequest(t, map[string]string{"big.txt": largeContent}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	// should get 413 or 400 (MaxBytesReader may cause parse error)
	assert.True(t, rr.Code == http.StatusRequestEntityTooLarge || rr.Code == http.StatusBadRequest,
		"expected 413 or 400, got %d", rr.Code)
}

func TestHandleUpload_Disabled(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: false, UploadMaxSize: 10 << 20}}

	req := createMultipartRequest(t, map[string]string{"test.txt": "data"}, map[string]string{"path": "."})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestHandleUpload_ExcludedPath(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755))

	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20, Exclude: []string{".git"}}}

	req := createMultipartRequest(t, map[string]string{"test.txt": "data"}, map[string]string{"path": ".git"})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestHandleUpload_Subdirectory(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	req := createMultipartRequest(t, map[string]string{"test.txt": "sub content"}, map[string]string{"path": "subdir"})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	content, err := os.ReadFile(filepath.Join(subDir, "test.txt"))
	require.NoError(t, err)
	assert.Equal(t, "sub content", string(content))
}

func TestHandleUpload_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	// create request with no files, just a path field
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("path", "."))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleUpload_NonexistentDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	req := createMultipartRequest(t, map[string]string{"test.txt": "data"}, map[string]string{"path": "nonexistent"})
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleUpload_InvalidFilename_Backslash(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	// build raw multipart body with backslash in filename
	boundary := "testboundary123"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"path\"\r\n\r\n.\r\n"+
		"--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"sub\\\\evil.txt\"\r\n"+
		"Content-Type: application/octet-stream\r\n\r\ndata\r\n--%s--\r\n",
		boundary, boundary, boundary)

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleUpload_DefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	srv := &Web{Config: Config{RootDir: tmpDir, EnableUpload: true, UploadMaxSize: 10 << 20}}

	// send request with no path field at all
	req := createMultipartRequest(t, map[string]string{"test.txt": "data"}, nil)
	rr := httptest.NewRecorder()
	srv.handleUpload(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// file should be in root dir
	content, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(content))
}

func TestValidateUploadPath(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0o755))

	srv := &Web{Config: Config{RootDir: tmpDir, Exclude: []string{".hidden"}}}

	// create a regular file to test "not a directory" case
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "afile.txt"), []byte("data"), 0o644))

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"root path", ".", false},
		{"subdirectory", "subdir", false},
		{"traversal with dot-dot", "../escape", true},
		{"absolute path", "/etc", true},
		{"nonexistent dir", "nope", true},
		{"excluded path", ".hidden", true},
		{"file not directory", "afile.txt", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.validateUploadPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateFilename(t *testing.T) {
	srv := &Web{}

	tests := []struct {
		name    string
		fname   string
		wantErr bool
	}{
		{"valid name", "file.txt", false},
		{"valid with spaces", "my file.txt", false},
		{"empty name", "", true},
		{"dot-dot", "../evil", true},
		{"slash", "sub/file", true},
		{"backslash", "sub\\file", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := srv.validateFilename(tt.fname)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
