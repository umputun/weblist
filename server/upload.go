package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// body bound above UploadMaxSize that covers the multipart preamble and the path field
const uploadOverheadBytes = 8 << 10

// uploadResponse represents the JSON response for upload operations
type uploadResponse struct {
	Uploaded []string `json:"uploaded,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// handleUpload handles file upload requests via multipart/form-data.
// it accepts one or more files and a target directory path, validates inputs,
// and writes files to the filesystem under RootDir.
func (wb *Web) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !wb.EnableUpload {
		wb.writeJSONError(w, http.StatusForbidden, "upload is disabled")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, wb.UploadMaxSize+uploadOverheadBytes)

	if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec // G120: body already bounded by MaxBytesReader above
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			wb.writeJSONError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		}
		log.Printf("[WARN] failed to parse multipart form: %v", err)
		wb.writeJSONError(w, http.StatusBadRequest, "failed to parse form data")
		return
	}

	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	// get and validate target directory path
	targetPath := r.FormValue("path")
	if targetPath == "" {
		targetPath = "."
	}

	cleanPath, err := wb.validateUploadPath(targetPath)
	if err != nil {
		wb.writeUploadError(w, err, "failed to validate upload path")
		return
	}

	// get uploaded files
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		wb.writeJSONError(w, http.StatusBadRequest, "no files provided")
		return
	}

	if err := wb.validateParts(cleanPath, files); err != nil {
		wb.writeUploadError(w, err, "failed to validate upload")
		return
	}

	if err := wb.ensureUploadDir(cleanPath); err != nil {
		wb.writeUploadError(w, err, "failed to create upload directory")
		return
	}

	uploaded, err := wb.storeUploadedFiles(cleanPath, files)
	if err != nil {
		wb.writeUploadError(w, err, "failed to save file")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(uploadResponse{Uploaded: uploaded}); err != nil {
		log.Printf("[ERROR] failed to encode upload response: %v", err)
	}
}

func (wb *Web) validateParts(cleanPath string, files []*multipart.FileHeader) error {
	for _, fh := range files {
		if err := wb.validateFilename(fh.Filename); err != nil {
			return &uploadError{http.StatusBadRequest, fmt.Sprintf("invalid filename %q: %v", fh.Filename, err)}
		}
		if wb.shouldExclude(filepath.Join(cleanPath, fh.Filename)) {
			return &uploadError{http.StatusForbidden, fmt.Sprintf("access denied to %q", fh.Filename)}
		}
		if fh.Size > wb.UploadMaxSize {
			return &uploadError{http.StatusRequestEntityTooLarge, fmt.Sprintf("file %q exceeds maximum size", fh.Filename)}
		}
	}
	return nil
}

func (wb *Web) storeUploadedFiles(cleanPath string, files []*multipart.FileHeader) ([]string, error) {
	uploaded := make([]string, 0, len(files))
	for _, fh := range files {
		destPath := filepath.Join(wb.RootDir, cleanPath, fh.Filename)

		src, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to read uploaded file %q: %w", fh.Filename, err)
		}

		if err := wb.writeUploadedFile(destPath, src, wb.UploadOverwrite); err != nil {
			_ = src.Close()
			return nil, fmt.Errorf("failed to save file %q: %w", fh.Filename, err)
		}
		_ = src.Close()

		uploaded = append(uploaded, fh.Filename)
		log.Printf("[INFO] uploaded file %q to %s", fh.Filename, destPath)
	}
	return uploaded, nil
}

// writeUploadError maps an *uploadError to its status, anything else to 500 with fallback as the message
func (wb *Web) writeUploadError(w http.ResponseWriter, err error, fallback string) {
	if ue, ok := errors.AsType[*uploadError](err); ok {
		wb.writeJSONError(w, ue.status, ue.Error())
		return
	}
	log.Printf("[ERROR] %s: %v", fallback, err)
	wb.writeJSONError(w, http.StatusInternalServerError, fallback)
}

// uploadError is an error type that carries an HTTP status code
type uploadError struct {
	status int
	msg    string
}

func (e *uploadError) Error() string { return e.msg }

// validateUploadPath cleans and validates the target directory path for upload.
// it returns the cleaned path relative to RootDir, or an uploadError with an appropriate HTTP status code.
func (wb *Web) validateUploadPath(path string) (string, error) {
	// clean the path
	cleanPath := filepath.ToSlash(filepath.Clean(path))

	// reject absolute paths
	if filepath.IsAbs(path) {
		return "", &uploadError{http.StatusBadRequest, "absolute paths are not allowed"}
	}

	// reject path traversal attempts
	if strings.Contains(cleanPath, "..") {
		return "", &uploadError{http.StatusBadRequest, "path traversal is not allowed"}
	}

	// check against exclude patterns
	if wb.shouldExclude(cleanPath) {
		return "", &uploadError{http.StatusForbidden, "access denied to target directory"}
	}

	ancestor, missing, err := wb.existingAncestor(cleanPath)
	if err != nil {
		return "", err
	}
	for _, component := range missing {
		if err := wb.validateFilename(component); err != nil {
			return "", &uploadError{http.StatusBadRequest, fmt.Sprintf("invalid directory name %q: %v", component, err)}
		}
	}

	// resolve symlinks on the deepest existing ancestor and verify its real path is still within RootDir
	realTarget, err := filepath.EvalSymlinks(filepath.Join(wb.RootDir, ancestor))
	if err != nil {
		return "", &uploadError{http.StatusBadRequest, fmt.Sprintf("cannot resolve target path: %s", cleanPath)}
	}
	realRoot, err := filepath.EvalSymlinks(wb.RootDir)
	if err != nil {
		return "", &uploadError{http.StatusInternalServerError, "cannot resolve root directory"}
	}

	// ensure resolved target is within resolved root
	if realTarget != realRoot && !strings.HasPrefix(realTarget, realRoot+string(filepath.Separator)) {
		return "", &uploadError{http.StatusBadRequest, "path traversal is not allowed"}
	}

	return cleanPath, nil
}

// os.Stat rather than Lstat: a symlinked directory inside the root is a valid target today and stays one
func (wb *Web) existingAncestor(cleanPath string) (ancestor string, missing []string, err error) {
	current := cleanPath
	for {
		info, statErr := os.Stat(filepath.Join(wb.RootDir, current))
		if statErr == nil {
			if !info.IsDir() {
				return "", nil, &uploadError{http.StatusBadRequest, "target path is not a directory"}
			}
			return current, missing, nil
		}
		if current == "." {
			return ".", missing, nil
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = filepath.Dir(current)
	}
}

// an existing directory is success: concurrent per-file requests into one new folder must not fail each other
func (wb *Web) ensureUploadDir(cleanPath string) error {
	if err := os.MkdirAll(filepath.Join(wb.RootDir, cleanPath), 0o750); err != nil {
		return fmt.Errorf("failed to create upload directory %q: %w", cleanPath, err)
	}
	return nil
}

// validateFilename checks that a filename is safe for writing
func (wb *Web) validateFilename(name string) error {
	if name == "" {
		return fmt.Errorf("filename is empty")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("contains '..'")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("contains path separator")
	}
	return nil
}

// writeUploadedFile writes the uploaded content to the destination path.
// when overwrite is false, it uses O_EXCL to atomically fail if the file exists.
// on write failure for non-overwrite mode, newly created files are removed.
// in overwrite mode, cleanup is skipped to avoid removing files written by concurrent requests.
func (wb *Web) writeUploadedFile(destPath string, src io.Reader, overwrite bool) error {
	// reject symlinks in overwrite mode to prevent writing outside RootDir
	if overwrite {
		if fi, err := os.Lstat(destPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return &uploadError{http.StatusBadRequest, fmt.Sprintf("refusing to overwrite symlink: %s", filepath.Base(destPath))}
		}
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}

	dst, err := os.OpenFile(destPath, flags, 0o644) //nolint:gosec // path is validated by validateUploadPath
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &uploadError{http.StatusConflict, fmt.Sprintf("file %q already exists", filepath.Base(destPath))}
		}
		return fmt.Errorf("failed to create file: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		if !overwrite {
			_ = os.Remove(destPath) // safe to remove: O_EXCL guarantees we created this file
		}
		return fmt.Errorf("failed to write file: %w", err)
	}

	if err := dst.Close(); err != nil {
		if !overwrite {
			_ = os.Remove(destPath) // safe to remove: O_EXCL guarantees we created this file
		}
		return fmt.Errorf("failed to close file: %w", err)
	}
	return nil
}
