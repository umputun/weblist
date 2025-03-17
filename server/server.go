package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/didip/tollbooth/v8"
	"github.com/go-pkgz/lgr"
	"github.com/go-pkgz/rest"
	"github.com/go-pkgz/rest/logger"

	"github.com/go-pkgz/routegroup"
)

//go:embed templates/* assets/*
var content embed.FS

// Web represents the web server.
type Web struct {
	Config
	FS fs.FS
}

// Config represents server configuration.
type Config struct {
	ListenAddr string
	Theme      string
	HideFooter bool
	RootDir    string
	Version    string
}

// Run starts the web server.
func (wb *Web) Run(ctx context.Context) error {
	// create router and set up routes
	mux := http.NewServeMux()
	router := routegroup.New(mux)

	router.Use(rest.Trace, rest.RealIP, rest.Recoverer(lgr.Default()))
	router.Use(rest.Throttle(1000))
	router.Use(tollbooth.HTTPMiddleware(tollbooth.NewLimiter(50, nil)))
	router.Use(rest.SizeLimit(1024 * 1024)) // 1M max request size
	router.Use(logger.New(logger.Log(lgr.Default()), logger.Prefix("[DEBUG]")).Handler)
	router.Use(rest.AppInfo("weblist", "umputun", wb.Version), rest.Ping)

	// serve static assets from embedded filesystem
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		return fmt.Errorf("failed to load embedded assets: %w", err)
	}

	router.HandleFunc("GET /", wb.handleRoot)
	router.HandleFunc("GET /partials/dir-contents", wb.handleDirContents)
	router.HandleFunc("GET /download/", wb.handleDownload)
	router.HandleFiles("/assets/", http.FS(assetsFS))

	srv := &http.Server{
		Addr:              wb.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// channel to capture server errors
	serverErrors := make(chan error, 1)

	// start server in a goroutine
	go func() {
		log.Printf("[INFO] starting server on %s with theme: %s, serving from: %s", wb.ListenAddr, wb.Theme, wb.RootDir)
		serverErrors <- srv.ListenAndServe()
	}()

	// wait for context cancellation or server error
	select {
	case err := <-serverErrors:
		return fmt.Errorf("[ERROR] server failed: %w", err)
	case <-ctx.Done():
		// gracefully shutdown when context is canceled
		log.Printf("[DEBUG] server shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("[WARN] graceful shutdown failed: %w", err)
		}
		log.Printf("[INFO] server shutdown completed")
		return nil
	}
}

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

// handleDownload serves file downloads
func (wb *Web) handleDownload(w http.ResponseWriter, r *http.Request) {
	// extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/download/")

	// remove trailing slash if present - this helps handle URLs like /download/templates/
	filePath = strings.TrimSuffix(filePath, "/")

	// clean the path to avoid directory traversal
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	if filePath == "." {
		filePath = ""
	}

	log.Printf("[DEBUG] download request for: %s", filePath)

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("file not found: %s", filepath.Base(filePath)), http.StatusNotFound)
		return
	}

	// if it's a directory, return an error
	if fileInfo.IsDir() {
		http.Error(w, "cannot download directories", http.StatusBadRequest)
		return
	}

	// open the file directly from the filesystem
	file, err := wb.FS.Open(filePath)
	if err != nil {
		http.Error(w, "error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// force all files to download instead of being displayed in browser
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileInfo.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// copy the file to the response - directly use file as ReadSeeker
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file.(io.ReadSeeker))
}

// handleDirContents renders partial directory contents for HTMX requests
func (wb *Web) handleDirContents(w http.ResponseWriter, r *http.Request) {
	// get directory path from query parameters
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortBy == "" {
		sortBy = "name" // default sort
	}
	if sortDir == "" {
		sortDir = "asc" // default direction
	}

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

	// get the file list
	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// parse template
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	data := map[string]interface{}{
		"Files":       fileList,
		"Path":        path,
		"DisplayPath": displayPath,
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   wb.getPathParts(path, sortBy, sortDir),
		"Theme":       wb.Config.Theme,
	}

	// execute the page-content template for HTMX requests
	if err := tmpl.ExecuteTemplate(w, "page-content", data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderFullPage renders the complete HTML page
func (wb *Web) renderFullPage(w http.ResponseWriter, r *http.Request, path string) {
	// clean the path to avoid directory traversal attacks
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path exists
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		http.Error(w, fmt.Sprintf("path not found: %s - %v", path, err), http.StatusNotFound)
		return
	}

	// if it's not a directory, redirect to download handler
	if !fileInfo.IsDir() {
		http.Redirect(w, r, "/download/"+path, http.StatusSeeOther)
		return
	}

	// parse templates from embedded filesystem
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortBy == "" {
		sortBy = "name" // default sort
	}
	if sortDir == "" {
		sortDir = "asc" // default direction
	}

	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	data := map[string]any{
		"Files":       fileList,
		"Path":        path,
		"DisplayPath": displayPath,
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   wb.getPathParts(path, sortBy, sortDir),
		"Theme":       wb.Config.Theme,
		"HideFooter":  wb.Config.HideFooter,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// getFileList returns a list of files in the given directory
func (wb *Web) getFileList(path, sortBy, sortDir string) ([]FileInfo, error) {
	// ensure the path is properly formatted for fs.ReadDir
	if path == "" {
		path = "."
	}

	entries, err := fs.ReadDir(wb.FS, path)
	if err != nil {
		return nil, err
	}

	var files []FileInfo //nolint // we can't preallocate the size here
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // skip files that can't be stat'd
		}

		entryPath := filepath.Join(path, entry.Name())
		// convert to slash for consistent paths
		entryPath = filepath.ToSlash(entryPath)

		fileInfo := FileInfo{
			Name:         entry.Name(),
			IsDir:        entry.IsDir(),
			Size:         info.Size(),
			LastModified: info.ModTime(),
			Path:         entryPath,
		}
		files = append(files, fileInfo)
	}

	// sort the file list
	wb.sortFiles(files, sortBy, sortDir)

	// special case: Add parent directory if not in root
	if path != "." {
		parentPath := filepath.Dir(path)

		// convert to slash for consistent paths
		parentPath = filepath.ToSlash(parentPath)
		if parentPath == "." {
			parentPath = ""
		}

		// get parent directory info if possible
		var lastModified time.Time
		parentInfo, err := fs.Stat(wb.FS, parentPath)
		if err == nil {
			lastModified = parentInfo.ModTime()
		} else {
			// use current time as fallback
			lastModified = time.Now()
		}

		// create parent directory entry
		parentDir := FileInfo{
			Name:         "..",
			IsDir:        true,
			Path:         parentPath,
			LastModified: lastModified,
			Size:         0,
		}

		// insert at the beginning
		files = append([]FileInfo{parentDir}, files...)
	}

	return files, nil
}

// sortFiles sorts the file list based on the specified criteria
func (wb *Web) sortFiles(files []FileInfo, sortBy, sortDir string) {
	// first separate directories and files
	sort.SliceStable(files, func(i, j int) bool {
		// special case: ".." always comes first
		if files[i].Name == ".." {
			return true
		}
		if files[j].Name == ".." {
			return false
		}

		if files[i].IsDir && !files[j].IsDir {
			return true
		}
		if !files[i].IsDir && files[j].IsDir {
			return false
		}

		// both are directories or both are files
		var result bool

		// sort based on the specified field
		switch sortBy {
		case "name":
			result = strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		case "date":
			result = files[i].LastModified.Before(files[j].LastModified)
		case "size":
			result = files[i].Size < files[j].Size
		default:
			result = strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		}

		// reverse if descending order is requested
		if sortDir == "desc" {
			result = !result
		}

		return result
	})
}

// getPathParts splits a path into parts for breadcrumb navigation
func (wb *Web) getPathParts(path, sortBy, sortDir string) []map[string]string {
	if path == "." {
		return []map[string]string{}
	}

	// convert path separators to slashes for consistent handling
	path = filepath.ToSlash(path)

	parts := strings.Split(path, "/")
	result := make([]map[string]string, 0, len(parts))

	// build the breadcrumb parts
	var currentPath string
	for _, part := range parts {
		if part == "" {
			continue
		}

		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		result = append(result, map[string]string{
			"Name": part,
			"Path": currentPath,
			"Sort": sortBy,  // add sort parameter
			"Dir":  sortDir, // add direction parameter
		})
	}

	return result
}
