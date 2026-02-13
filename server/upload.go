package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

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

	// apply size limit to the request body
	r.Body = http.MaxBytesReader(w, r.Body, wb.UploadMaxSize)

	// parse multipart form with 10MB in-memory buffer
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			wb.writeJSONError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		}
		wb.writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse form: %v", err))
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
		var ue *uploadError
		if errors.As(err, &ue) {
			wb.writeJSONError(w, ue.status, ue.Error())
		} else {
			wb.writeJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// get uploaded files
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		wb.writeJSONError(w, http.StatusBadRequest, "no files provided")
		return
	}

	var uploaded []string
	for _, fh := range files {
		// validate filename
		if err := wb.validateFilename(fh.Filename); err != nil {
			wb.writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid filename %q: %v", fh.Filename, err))
			return
		}

		destPath := filepath.Join(wb.RootDir, cleanPath, fh.Filename)

		// open the uploaded file
		src, err := fh.Open()
		if err != nil {
			wb.writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read uploaded file: %v", err))
			return
		}

		// write the file to disk
		if err := wb.writeUploadedFile(destPath, src, wb.UploadOverwrite); err != nil {
			_ = src.Close()
			var ue *uploadError
			if errors.As(err, &ue) {
				wb.writeJSONError(w, ue.status, ue.Error())
			} else {
				wb.writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save file: %v", err))
			}
			return
		}
		_ = src.Close()

		uploaded = append(uploaded, fh.Filename)
		log.Printf("[INFO] uploaded file %q to %s", fh.Filename, destPath)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(uploadResponse{Uploaded: uploaded}); err != nil {
		log.Printf("[ERROR] failed to encode upload response: %v", err)
	}
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

	// verify the target directory exists and is within RootDir
	absTarget := filepath.Join(wb.RootDir, cleanPath)
	absTarget = filepath.Clean(absTarget)

	// check that target directory exists
	info, err := os.Stat(absTarget)
	if err != nil {
		return "", &uploadError{http.StatusBadRequest, fmt.Sprintf("target directory does not exist: %s", cleanPath)}
	}
	if !info.IsDir() {
		return "", &uploadError{http.StatusBadRequest, "target path is not a directory"}
	}

	// resolve symlinks and verify real path is still within RootDir
	realTarget, err := filepath.EvalSymlinks(absTarget)
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
