package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// handleRoot displays the root directory listing
func (wb *Web) handleRoot(w http.ResponseWriter, r *http.Request) {
	// get path from query parameter, default to empty string (root of locked filesystem)
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))
	wb.renderFullPage(w, r, path)
}

// handleDirContents renders partial directory contents for HTMX requests
func (wb *Web) handleDirContents(w http.ResponseWriter, r *http.Request) {
	// redirect non-HTMX requests to main page for proper full-page rendering
	if !wb.isHTMXRequest(r) {
		http.Redirect(w, r, "/?"+r.URL.RawQuery, http.StatusFound)
		return
	}

	// get directory path from query parameters
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	// get sort parameters from query or cookies
	sortBy, sortDir := wb.getSortParams(w, r)

	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path exists and is a directory
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		http.Error(w, fmt.Sprintf("directory not found: %v", err), http.StatusNotFound)
		return
	}

	if !fileInfo.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}

	// use cached template

	// get the directory file list
	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("error reading directory: %v", err), http.StatusInternalServerError)
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	// prepare data with struct directly in this function
	isAuthenticated := false
	if wb.Auth != "" {
		isAuthenticated = wb.isAuthenticatedByCookie(r)
	}

	data := struct {
		Files             []FileInfo
		Path              string
		DisplayPath       string
		SortBy            string
		SortDir           string
		PathParts         []map[string]string
		Theme             string
		Title             string
		IsAuthenticated   bool
		BrandName         string
		BrandColor        string
		CustomFooter      string
		EnableMultiSelect bool
		EnableUpload      bool
		UploadMaxSize     int64
	}{
		Files:             fileList,
		Path:              path,
		DisplayPath:       displayPath,
		SortBy:            sortBy,
		SortDir:           sortDir,
		PathParts:         wb.getPathParts(path, sortBy, sortDir),
		Theme:             wb.Theme,
		BrandName:         wb.BrandName,
		BrandColor:        wb.BrandColor,
		Title:             wb.Title,
		IsAuthenticated:   isAuthenticated,
		CustomFooter:      wb.CustomFooter,
		EnableMultiSelect: wb.EnableMultiSelect,
		EnableUpload:      wb.EnableUpload,
		UploadMaxSize:     wb.UploadMaxSize,
	}

	// execute just the page-content template
	if err := wb.templates.indexTemplate.ExecuteTemplate(w, "page-content", data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleViewFile serves a file view for text files
func (wb *Web) handleViewFile(w http.ResponseWriter, r *http.Request) {
	// extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/view/")

	// clean the path to avoid directory traversal
	filePath = filepath.ToSlash(filepath.Clean(filePath))

	// check if the path should be excluded
	if wb.shouldExclude(filePath) {
		http.Error(w, "access denied to requested file", http.StatusForbidden)
		return
	}

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, filePath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// if it's a directory, return an error
	if fileInfo.IsDir() {
		http.Error(w, "cannot view directories", http.StatusBadRequest)
		return
	}

	// open the file
	file, err := wb.FS.Open(filePath)
	if err != nil {
		http.Error(w, "error opening file", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	// determine content type and file properties
	ctInfo := DetermineContentType(filePath)

	// handle non-text files (images, PDFs, etc.)
	if !ctInfo.IsText {
		w.Header().Set("Content-Type", ctInfo.MIMEType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
		http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file.(io.ReadSeeker))
		return
	}

	// from here, we're only handling text files

	// read file content
	fileContent, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}

	// use template for viewing
	w.Header().Set("Content-Type", "text/html")

	// determine theme from query param, fall back to server default
	theme := r.URL.Query().Get("theme")
	if theme == "" {
		theme = wb.Theme
	}

	// parse templates

	// prepare data for the template
	data := struct {
		FileName string
		Content  string
		Theme    string
		IsHTML   bool
	}{
		FileName: fileInfo.Name(),
		Content:  string(fileContent),
		Theme:    theme,
		IsHTML:   ctInfo.IsHTML,
	}

	// apply syntax highlighting for non-HTML text files if enabled
	if !ctInfo.IsHTML && wb.EnableSyntaxHighlighting {
		highlighted, err := wb.highlightCode(string(fileContent), fileInfo.Name(), theme)
		if err != nil {
			log.Printf("[WARN] failed to highlight code: %v", err)
			// fall back to plain text if highlighting fails
		} else {
			data.Content = highlighted
		}
	}

	// execute the file-view template
	if err := wb.templates.fileTemplate.ExecuteTemplate(w, "file-view", data); err != nil {
		log.Printf("[ERROR] failed to execute file-view template: %v", err)
		http.Error(w, "error rendering file view", http.StatusInternalServerError)
	}
}

// handleDownload serves file downloads
func (wb *Web) handleDownload(w http.ResponseWriter, r *http.Request) {
	// extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/")

	// remove trailing slash if present - this helps handle URLs like /download/templates/
	filePath = strings.TrimSuffix(filePath, "/")

	// clean the path to avoid directory traversal
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	if filePath == "." {
		filePath = ""
	}
	log.Printf("[DEBUG] download request for: %s", filePath)

	// check if the file should be excluded
	if wb.shouldExclude(filePath) {
		http.Error(w, "access denied to requested file", http.StatusForbidden)
		return
	}

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, filePath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// if it's a directory, redirect to directory view
	if fileInfo.IsDir() {
		http.Redirect(w, r, "/?path="+filePath, http.StatusSeeOther)
		return
	}

	// open the file directly from the filesystem
	file, err := wb.FS.Open(filePath)
	if err != nil {
		http.Error(w, "error opening file", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	// force all files to download instead of being displayed in browser
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileInfo.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// copy the file to the response - directly use file as ReadSeeker
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file.(io.ReadSeeker))
}

// handleFileModal renders the modal with embedded file content
func (wb *Web) handleFileModal(w http.ResponseWriter, r *http.Request) {
	// get file path from query parameter
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "file path not provided", http.StatusBadRequest)
		return
	}

	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path should be excluded
	if wb.shouldExclude(path) {
		http.Error(w, fmt.Sprintf("access denied: %s", filepath.Base(path)), http.StatusForbidden)
		return
	}

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	if fileInfo.IsDir() {
		http.Error(w, "cannot display directories in modal", http.StatusBadRequest)
		return
	}

	// open the file (needed for reading content later if needed)
	file, err := wb.FS.Open(path)
	if err != nil {
		http.Error(w, "error opening file", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	// determine content type and file properties
	ctInfo := DetermineContentType(path)

	// prepare data for the modal template
	data := struct {
		FileName    string
		FilePath    string
		ContentType string
		FileSize    int64
		IsImage     bool
		IsPDF       bool
		IsText      bool
		IsHTML      bool
		Theme       string
	}{
		FileName:    fileInfo.Name(),
		FilePath:    path,
		ContentType: ctInfo.MIMEType,
		FileSize:    fileInfo.Size(),
		IsImage:     ctInfo.IsImage,
		IsPDF:       ctInfo.IsPDF,
		IsText:      ctInfo.IsText,
		IsHTML:      ctInfo.IsHTML,
		Theme:       wb.Theme,
	}

	// parse templates

	// set content type and execute the file-modal template
	w.Header().Set("Content-Type", "text/html")
	if err := wb.templates.fileTemplate.ExecuteTemplate(w, "file-modal", data); err != nil {
		log.Printf("[ERROR] failed to execute file-modal template: %v", err)
		http.Error(w, "error rendering file modal", http.StatusInternalServerError)
	}
}

// handleSelectionStatus processes selection status updates from checkboxes
// and returns the partial HTML for the selection status component
func (wb *Web) handleSelectionStatus(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// get all selected files from the form
	selectedFiles := r.Form["selected-files"]
	selectAll := r.FormValue("select-all")

	// toggle logic for select-all
	var checkState bool
	if selectAll == "true" {
		// if select-all is clicked, we need to toggle between selected and unselected
		// get the total number of files from the form
		totalFilesStr := r.FormValue("total-files")
		totalFiles, err := strconv.Atoi(totalFilesStr)
		if err != nil {
			http.Error(w, "Invalid total-files value", http.StatusBadRequest)
			return
		}

		// check if we're toggling from "all selected" to "none selected" state
		// if the number of selected files matches the total, we're in "all selected" state
		if len(selectedFiles) == totalFiles {
			// toggle to "none selected" state
			checkState = false
			selectedFiles = []string{} // clear the selection
		} else {
			// otherwise, we're toggling from "none selected" or "partially selected" to "all selected"
			// get path values for all files in the current directory
			selectedFiles = r.Form["path-values"]
			// set checkboxes to checked state
			checkState = true
		}
	} else {
		// regular checkbox update - just use the current selection
		checkState = len(selectedFiles) > 0
	}

	// prepare template data
	data := struct {
		Count         int
		SelectedFiles []string
		SelectAll     bool
		CheckState    bool
	}{
		Count:         len(selectedFiles),
		SelectedFiles: selectedFiles,
		SelectAll:     selectAll == "true",
		CheckState:    checkState,
	}

	// execute the selection-status template
	w.Header().Set("Content-Type", "text/html")
	// trigger checkbox sync event when select-all is clicked
	if selectAll == "true" {
		w.Header().Set("HX-Trigger", "updateCheckboxes")
	}
	if err := wb.templates.indexTemplate.ExecuteTemplate(w, "selection-status", data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleDownloadSelected creates a zip file of selected files and sends it to the client
func (wb *Web) handleDownloadSelected(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// get selected files from form
	selectedFiles := r.Form["selected-files"]
	if len(selectedFiles) == 0 {
		http.Error(w, "No files selected", http.StatusBadRequest)
		return
	}

	// set up response headers for the ZIP file
	timestamp := time.Now().Format("20060102-150405")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="weblist-files-%s.zip"`, timestamp))

	// create the ZIP file directly on the response writer
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	// process each selected file
	for _, filePath := range selectedFiles {
		// clean the path to avoid directory traversal
		filePath = filepath.ToSlash(filepath.Clean(filePath))

		// check if the path should be excluded
		if wb.shouldExclude(filePath) {
			log.Printf("[WARN] skipping excluded file in ZIP: %s", filePath)
			continue
		}

		// check if the file exists
		fileInfo, err := fs.Stat(wb.FS, filePath)
		if err != nil {
			log.Printf("[ERROR] file not found for ZIP: %s: %v", filePath, err)
			continue
		}

		// if it's a directory, add all its contents recursively
		if fileInfo.IsDir() {
			err = wb.addDirectoryToZip(zipWriter, filePath, "")
			if err != nil {
				log.Printf("[ERROR] failed to add directory to ZIP: %s: %v", filePath, err)
			}
			continue
		}

		// add the file to the ZIP
		err = wb.addFileToZip(zipWriter, filePath, "")
		if err != nil {
			log.Printf("[ERROR] failed to add file to ZIP: %s: %v", filePath, err)
		}
	}
}

// addFileToZip adds a single file to the ZIP archive
func (wb *Web) addFileToZip(zipWriter *zip.Writer, filePath, zipPath string) error {
	// open the file
	file, err := wb.FS.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// get file info
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file stats: %w", err)
	}

	// if zipPath is empty, use the original file name
	if zipPath == "" {
		zipPath = filepath.Base(filePath)
	}

	// create a new file header
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("failed to create ZIP header: %w", err)
	}

	// set the name in the ZIP
	header.Name = zipPath
	header.Method = zip.Deflate // use compression

	// create the file in the ZIP
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("failed to create ZIP entry: %w", err)
	}

	// copy the file content to the ZIP
	_, err = io.Copy(writer, file)
	if err != nil {
		return fmt.Errorf("failed to write file to ZIP: %w", err)
	}

	return nil
}

// addDirectoryToZip recursively adds a directory and its contents to the ZIP
func (wb *Web) addDirectoryToZip(zipWriter *zip.Writer, dirPath, zipPath string) error {
	// read the directory contents
	entries, err := fs.ReadDir(wb.FS, dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	// process each entry
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())

		// skip excluded paths
		if wb.shouldExclude(entryPath) {
			continue
		}

		// create the path within the ZIP
		entryZipPath := entry.Name()
		if zipPath != "" {
			entryZipPath = filepath.Join(zipPath, entry.Name())
		}

		// if it's a directory, recursively add its contents
		if entry.IsDir() {
			// create directory entry in ZIP
			_, err := zipWriter.Create(entryZipPath + "/")
			if err != nil {
				log.Printf("[WARN] failed to create directory in ZIP: %s: %v", entryZipPath, err)
			}

			// add contents recursively
			err = wb.addDirectoryToZip(zipWriter, entryPath, entryZipPath)
			if err != nil {
				log.Printf("[WARN] failed to add directory contents to ZIP: %s: %v", entryPath, err)
			}
		} else {
			// add the file to the ZIP
			err := wb.addFileToZip(zipWriter, entryPath, entryZipPath)
			if err != nil {
				log.Printf("[WARN] failed to add file to ZIP: %s: %v", entryPath, err)
			}
		}
	}

	return nil
}

// writeJSONError writes a JSON error response with the specified status code
func (wb *Web) writeJSONError(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": errMsg}); err != nil {
		log.Printf("[ERROR] failed to encode error response: %v", err)
	}
}

// parseSortParams extracts and validates sort parameters from the request
func (wb *Web) parseSortParams(sortParam string) (sortBy, sortDir string) {
	// default values
	sortBy = "name"
	sortDir = "asc"

	if sortParam == "" {
		return sortBy, sortDir
	}

	// parse direction from prefix
	if strings.HasPrefix(sortParam, "+") {
		sortDir = "asc"
		sortParam = sortParam[1:]
	} else if strings.HasPrefix(sortParam, "-") {
		sortDir = "desc"
		sortParam = sortParam[1:]
	}

	// mapping of valid sort parameters to internal field names
	sortFieldMap := map[string]string{
		"name":  "name",
		"size":  "size",
		"mtime": "date", // mtime maps to date internally
	}

	// check if requested sort field is valid
	if internalField, ok := sortFieldMap[sortParam]; ok {
		sortBy = internalField
	}

	return sortBy, sortDir
}

// fileResponse represents a file entry for JSON response
type fileResponse struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	IsDir        bool      `json:"is_dir"`
	Size         int64     `json:"size"`
	SizeHuman    string    `json:"size_human,omitempty"`
	LastModified time.Time `json:"last_modified"`
	TimeStr      string    `json:"time_str,omitempty"`
	IsViewable   bool      `json:"is_viewable,omitempty"`
}

// handleAPIList handles API requests for listing files with JSON response
// It supports query parameters:
// - path: the directory path to list (defaults to root if not provided)
// - sort: sort criteria with direction prefix (e.g., +name, -size, +mtime)
func (wb *Web) handleAPIList(w http.ResponseWriter, r *http.Request) {
	// get path from query parameter, default to root directory
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}
	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path exists and is a directory
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		wb.writeJSONError(w, http.StatusNotFound, fmt.Sprintf("directory not found: %v", err))
		return
	}

	if !fileInfo.IsDir() {
		wb.writeJSONError(w, http.StatusBadRequest, "not a directory")
		return
	}

	// parse the sort parameter
	sortParam := r.URL.Query().Get("sort")
	sortBy, sortDir := wb.parseSortParams(sortParam)

	// get the file list
	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		wb.writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("error reading directory: %v", err))
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	// prepare file list for JSON response
	files := make([]fileResponse, 0, len(fileList))
	for _, f := range fileList {
		files = append(files, fileResponse{
			Name:         f.Name,
			Path:         f.Path,
			IsDir:        f.IsDir,
			Size:         f.Size,
			SizeHuman:    f.SizeToString(),
			LastModified: f.LastModified,
			TimeStr:      f.TimeString(),
			IsViewable:   f.IsViewable(),
		})
	}

	// determine response sort parameter based on original query parameter
	responseSortBy := "name" // default

	// original query parameter takes precedence for the response
	if strings.Contains(sortParam, "size") {
		responseSortBy = "size"
	} else if strings.Contains(sortParam, "mtime") {
		responseSortBy = "date" // mtime is represented as date in the UI
	} else if strings.Contains(sortParam, "name") || sortParam == "" {
		responseSortBy = "name"
	}

	// create the response
	response := struct {
		Path  string         `json:"path"`
		Files []fileResponse `json:"files"`
		Sort  string         `json:"sort"`
		Dir   string         `json:"dir"`
	}{
		Path:  displayPath,
		Files: files,
		Sort:  responseSortBy,
		Dir:   sortDir,
	}

	// set content type and encode to JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		wb.writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("error encoding JSON: %v", err))
	}
}
