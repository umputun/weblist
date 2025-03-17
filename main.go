package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-pkgz/routegroup"
)

//go:embed templates/* assets/*
var content embed.FS

// FileInfo represents a file or directory to be displayed in the list
type FileInfo struct {
	Name         string
	IsDir        bool
	Size         int64
	LastModified time.Time
	Path         string
}

// SizeToString converts file size to human-readable format
func (f FileInfo) SizeToString() string {
	if f.IsDir {
		return "-"
	}

	const unit = 1024
	if f.Size < unit {
		return fmt.Sprintf("%d B", f.Size)
	}

	div, exp := int64(unit), 0
	for n := f.Size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(f.Size)/float64(div), "KMGTPE"[exp])
}

// TimeString formats the last modified time
func (f FileInfo) TimeString() string {
	return f.LastModified.Format("02-Jan-2006 15:04:05")
}

// Config represents application configuration
type Config struct {
	ListenAddr string
	Theme      string
	HideFooter bool
	RootDir    string
}

// App holds the application state and dependencies
type App struct {
	Config Config
	FS     fs.FS
}

func main() {
	// parse command-line flags
	cfg := Config{}
	flag.StringVar(&cfg.ListenAddr, "listen", ":8080", "Address to listen on")
	flag.StringVar(&cfg.Theme, "theme", "light", "Theme to use (light or dark)")
	flag.BoolVar(&cfg.HideFooter, "hide-footer", false, "Hide footer (true or false)")
	flag.StringVar(&cfg.RootDir, "root", ".", "Root directory to serve")
	flag.Parse()

	// validate theme
	if cfg.Theme != "light" && cfg.Theme != "dark" {
		log.Printf("Warning: Invalid theme '%s'. Using 'light' instead.", cfg.Theme)
		cfg.Theme = "light"
	}

	// get absolute path for root directory
	absRootDir, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		log.Fatalf("Failed to get absolute path for root directory: %v", err)
	}
	cfg.RootDir = absRootDir

	// create OS filesystem locked to the root directory
	rootFS := os.DirFS(cfg.RootDir)

	app := &App{
		Config: cfg,
		FS:     rootFS,
	}

	// create router and set up routes
	mux := http.NewServeMux()
	router := routegroup.New(mux)

	// serve static assets from embedded filesystem
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		log.Fatalf("Failed to load embedded assets: %v", err)
	}
	router.HandleFiles("/assets/", http.FS(assetsFS))

	// route registration in correct order
	router.HandleFunc("GET /", app.handleRoot)
	router.HandleFunc("GET /partials/dir-contents", app.handleDirContents)
	router.HandleFunc("GET /download/", app.handleDownload)

	// start server
	log.Printf("Starting server on %s with theme: %s, serving from: %s", cfg.ListenAddr, cfg.Theme, cfg.RootDir)
	if err := http.ListenAndServe(cfg.ListenAddr, router); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// handleRoot displays the root directory listing
func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	// get path from query parameter, default to empty string (root of locked filesystem)
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))
	app.renderFullPage(w, r, path)
}

// handleDownload serves file downloads
func (app *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	// extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/download/")

	// remove trailing slash if present - this helps handle URLs like /download/templates/
	filePath = strings.TrimSuffix(filePath, "/")

	// clean the path to avoid directory traversal
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	if filePath == "." {
		filePath = ""
	}

	// log the request for debugging
	log.Printf("Download request for: %s", filePath)

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(app.FS, filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("File not found: %s", filepath.Base(filePath)), http.StatusNotFound)
		return
	}

	if fileInfo.IsDir() {
		// check if index.html exists in this directory
		indexPath := filepath.Join(filePath, "index.html")
		indexInfo, err := fs.Stat(app.FS, indexPath)
		if err == nil && !indexInfo.IsDir() {
			// Create the physical file path for index.html
			physicalIndexPath := filepath.Join(app.Config.RootDir, indexPath)
			file, err := os.Open(physicalIndexPath)
			if err != nil {
				http.Error(w, "Error opening file", http.StatusInternalServerError)
				return
			}
			defer file.Close()

			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", "index.html"))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", indexInfo.Size()))

			http.ServeContent(w, r, "index.html", indexInfo.ModTime(), file)
			return
		}

		// if no index.html or it's also a directory, return error
		http.Error(w, "Cannot download directories", http.StatusBadRequest)
		return
	}

	// Because we need an io.ReadSeeker for ServeContent, we need to use the actual OS file
	// Create the physical file path by joining the root directory with the relative path
	physicalPath := filepath.Join(app.Config.RootDir, filePath)
	file, err := os.Open(physicalPath)
	if err != nil {
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// force all files to download instead of being displayed in browser
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileInfo.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// copy the file to the response
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file)
}

// handleDirContents renders partial directory contents for HTMX requests
func (app *App) handleDirContents(w http.ResponseWriter, r *http.Request) {
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
	fileInfo, err := fs.Stat(app.FS, path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Directory not found: %v", err), http.StatusNotFound)
		return
	}

	if !fileInfo.IsDir() {
		http.Error(w, "Not a directory", http.StatusBadRequest)
		return
	}

	// get the file list
	fileList, err := app.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "Error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// parse template
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
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
		"DisplayPath": displayPath, // use for display purposes
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   app.getPathParts(path),
		"Theme":       app.Config.Theme, // always use the CLI-specified theme
	}

	// execute the page-content template for HTMX requests
	if err := tmpl.ExecuteTemplate(w, "page-content", data); err != nil {
		http.Error(w, "Template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderFullPage renders the complete HTML page
func (app *App) renderFullPage(w http.ResponseWriter, r *http.Request, path string) {
	// clean the path to avoid directory traversal attacks
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path exists
	fileInfo, err := fs.Stat(app.FS, path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Path not found: %s - %v", path, err), http.StatusNotFound)
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
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
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

	fileList, err := app.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "Error reading directory: "+err.Error(), http.StatusInternalServerError)
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
		"DisplayPath": displayPath, // use for display purposes
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   app.getPathParts(path),
		"Theme":       app.Config.Theme, // use the CLI-specified theme
		"HideFooter":  app.Config.HideFooter,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// getFileList returns a list of files in the given directory
func (app *App) getFileList(path string, sortBy, sortDir string) ([]FileInfo, error) {
	// Ensure the path is properly formatted for fs.ReadDir
	if path == "" {
		path = "."
	}

	entries, err := fs.ReadDir(app.FS, path)
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // skip files that can't be stat'd
		}

		entryPath := filepath.Join(path, entry.Name())
		// Convert to slash for consistent paths
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
	app.sortFiles(files, sortBy, sortDir)

	// special case: Add parent directory if not in root
	if path != "." {
		parentPath := filepath.Dir(path)

		// Convert to slash for consistent paths
		parentPath = filepath.ToSlash(parentPath)
		if parentPath == "." {
			parentPath = ""
		}

		// Get parent directory info if possible
		var lastModified time.Time
		parentInfo, err := fs.Stat(app.FS, parentPath)
		if err == nil {
			lastModified = parentInfo.ModTime()
		} else {
			// Use current time as fallback
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
func (app *App) sortFiles(files []FileInfo, sortBy, sortDir string) {
	// first separate directories and files
	sort.SliceStable(files, func(i, j int) bool {
		// Special case: ".." always comes first
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
func (app *App) getPathParts(path string) []map[string]string {
	if path == "." {
		return []map[string]string{}
	}

	// Convert path separators to slashes for consistent handling
	path = filepath.ToSlash(path)

	parts := strings.Split(path, "/")
	result := make([]map[string]string, 0, len(parts))

	// Build the breadcrumb parts
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
		})
	}

	return result
}
