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
}

func main() {
	// Parse command-line flags
	cfg := Config{}
	flag.StringVar(&cfg.ListenAddr, "listen", ":8080", "Address to listen on")
	flag.StringVar(&cfg.Theme, "theme", "light", "Theme to use (light or dark)")
	flag.Parse()

	// Validate theme
	if cfg.Theme != "light" && cfg.Theme != "dark" {
		log.Printf("Warning: Invalid theme '%s'. Using 'light' instead.", cfg.Theme)
		cfg.Theme = "light"
	}

	// Create router and set up routes
	mux := http.NewServeMux()
	router := routegroup.New(mux)

	// Serve static assets from embedded filesystem
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		log.Fatalf("Failed to load embedded assets: %v", err)
	}
	router.HandleFiles("/assets/", http.FS(assetsFS))

	// Route registration in correct order
	router.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		handleRoot(w, r, cfg)
	})
	router.HandleFunc("GET /partials/dir-contents", func(w http.ResponseWriter, r *http.Request) {
		handleDirContents(w, r, cfg)
	})
	router.HandleFunc("GET /download/", handleDownload)

	// Start server
	log.Printf("Starting server on %s with theme: %s", cfg.ListenAddr, cfg.Theme)
	if err := http.ListenAndServe(cfg.ListenAddr, router); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// handleRoot displays the root directory listing
func handleRoot(w http.ResponseWriter, r *http.Request, cfg Config) {
	// Get path from query parameter, default to current directory
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	// Clean the path to avoid directory traversal
	path = filepath.Clean(path)
	renderFullPage(w, r, path, cfg.Theme)
}

// handleDownload serves file downloads
func handleDownload(w http.ResponseWriter, r *http.Request) {
	// Extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/download/")

	// Remove trailing slash if present - this helps handle URLs like /download/templates/
	filePath = strings.TrimSuffix(filePath, "/")

	// Clean the path to avoid directory traversal
	filePath = filepath.Clean(filePath)

	// Log the request for debugging
	log.Printf("Download request for: %s", filePath)

	// Check if the file exists and is not a directory
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("File not found: %s", filepath.Base(filePath)), http.StatusNotFound)
		return
	}

	if fileInfo.IsDir() {
		// Check if index.html exists in this directory
		indexPath := filepath.Join(filePath, "index.html")
		indexInfo, err := os.Stat(indexPath)
		if err == nil && !indexInfo.IsDir() {
			// Serve index.html
			file, err := os.Open(indexPath)
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

		// If no index.html or it's also a directory, return error
		http.Error(w, "Cannot download directories", http.StatusBadRequest)
		return
	}

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Force all files to download instead of being displayed in browser
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileInfo.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// Copy the file to the response
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file)
}

// handleDirContents renders partial directory contents for HTMX requests
func handleDirContents(w http.ResponseWriter, r *http.Request, cfg Config) {
	// Get directory path from query parameters
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortBy == "" {
		sortBy = "name" // Default sort
	}
	if sortDir == "" {
		sortDir = "asc" // Default direction
	}

	// Check if the path exists and is a directory
	fileInfo, err := os.Stat(path)
	if err != nil {
		http.Error(w, "Directory not found: "+err.Error(), http.StatusNotFound)
		return
	}

	if !fileInfo.IsDir() {
		http.Error(w, "Not a directory", http.StatusBadRequest)
		return
	}

	// Get the file list
	fileList, err := getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "Error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Parse template
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	data := map[string]interface{}{
		"Files":       fileList,
		"Path":        path,
		"DisplayPath": displayPath, // Use for display purposes
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   getPathParts(path),
		"Theme":       cfg.Theme, // Always use the CLI-specified theme
	}

	// Execute the page-content template for HTMX requests
	if err := tmpl.ExecuteTemplate(w, "page-content", data); err != nil {
		http.Error(w, "Template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderFullPage renders the complete HTML page
func renderFullPage(w http.ResponseWriter, r *http.Request, path string, theme string) {
	// Clean the path to avoid directory traversal attacks
	path = filepath.Clean(path)

	// Check if the path exists
	fileInfo, err := os.Stat(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Path not found: %s - %v", path, err), http.StatusNotFound)
		return
	}

	// If it's not a directory, redirect to download handler
	if !fileInfo.IsDir() {
		http.Redirect(w, r, "/download/"+path, http.StatusSeeOther)
		return
	}

	// Parse templates from embedded filesystem
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortBy == "" {
		sortBy = "name" // Default sort
	}
	if sortDir == "" {
		sortDir = "asc" // Default direction
	}

	fileList, err := getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "Error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	data := map[string]interface{}{
		"Files":       fileList,
		"Path":        path,
		"DisplayPath": displayPath, // Use for display purposes
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   getPathParts(path),
		"Theme":       theme, // Use the CLI-specified theme
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// getFileList returns a list of files in the given directory
func getFileList(path string, sortBy, sortDir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // Skip files that can't be stat'd
		}

		fileInfo := FileInfo{
			Name:         entry.Name(),
			IsDir:        entry.IsDir(),
			Size:         info.Size(),
			LastModified: info.ModTime(),
			Path:         filepath.Join(path, entry.Name()),
		}
		files = append(files, fileInfo)
	}

	// Sort the file list
	sortFiles(files, sortBy, sortDir)

	// Special case: Add parent directory if not in root
	if path != "." {
		// Get absolute paths to ensure we can stat the parent correctly
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path // Fallback to the original path
		}

		parentPath := filepath.Dir(path)
		absParentPath := filepath.Dir(absPath)

		// For Windows compatibility, convert backslashes to forward slashes
		parentPath = filepath.ToSlash(parentPath)
		if parentPath == "." {
			parentPath = ""
		}

		// Get the actual modification time of the parent directory
		var lastModified time.Time
		parentInfo, err := os.Stat(absParentPath)
		if err == nil {
			lastModified = parentInfo.ModTime()
		} else {
			// Try the non-absolute path as a fallback
			parentInfo, err = os.Stat(parentPath)
			if err == nil {
				lastModified = parentInfo.ModTime()
			} else {
				// Last resort - use current time
				log.Printf("Failed to get parent directory info for %s: %v", parentPath, err)
				lastModified = time.Now()
			}
		}

		// Create parent directory entry with correct timestamp
		parentDir := FileInfo{
			Name:         "..",
			IsDir:        true,
			Path:         parentPath,
			LastModified: lastModified,
			Size:         0,
		}

		// Insert at the beginning
		files = append([]FileInfo{parentDir}, files...)
	}

	return files, nil
}

// sortFiles sorts the file list based on the specified criteria
func sortFiles(files []FileInfo, sortBy, sortDir string) {
	// First separate directories and files
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].IsDir && !files[j].IsDir {
			return true
		}
		if !files[i].IsDir && files[j].IsDir {
			return false
		}

		// Both are directories or both are files
		var result bool

		// Sort based on the specified field
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

		// Reverse if descending order is requested
		if sortDir == "desc" {
			result = !result
		}

		return result
	})
}

// getPathParts splits a path into parts for breadcrumb navigation
func getPathParts(path string) []map[string]string {
	if path == "." {
		return []map[string]string{}
	}

	parts := strings.Split(path, string(os.PathSeparator))
	result := make([]map[string]string, 0, len(parts))

	for i, part := range parts {
		if part == "" {
			continue
		}

		currentPath := strings.Join(parts[:i+1], string(os.PathSeparator))
		result = append(result, map[string]string{
			"Name": part,
			"Path": currentPath,
		})
	}

	return result
}
