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

func main() {
	// Parse command-line flags
	listenAddr := flag.String("listen", ":8080", "Address to listen on")
	flag.Parse()

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
	router.HandleFunc("GET /", handleRoot)
	router.HandleFunc("GET /partials/dir-contents", handleDirContents)
	router.HandleFunc("GET /download/", handleDownload)

	// Start server
	log.Printf("Starting server on %s", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, router); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// handleRoot displays the root directory listing
func handleRoot(w http.ResponseWriter, r *http.Request) {
	// Get path from query parameter, default to current directory
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}

	// Clean the path to avoid directory traversal
	path = filepath.Clean(path)
	renderFullPage(w, r, path)
}

// handleDownload serves file downloads
func handleDownload(w http.ResponseWriter, r *http.Request) {
	// Extract the file path from the URL
	filePath := strings.TrimPrefix(r.URL.Path, "/download/")

	// Log the request for debugging
	log.Printf("Download request for: %s", filePath)

	// Check if the file exists and is not a directory
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("File not found: %s - %v", filePath, err), http.StatusNotFound)
		return
	}

	if fileInfo.IsDir() {
		http.Error(w, "Cannot download directories", http.StatusBadRequest)
		return
	}

	// Set content-disposition header for download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileInfo.Name()))

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// handleDirContents renders partial directory contents for HTMX requests
func handleDirContents(w http.ResponseWriter, r *http.Request) {
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
	}

	// Execute the page-content template for HTMX requests
	if err := tmpl.ExecuteTemplate(w, "page-content", data); err != nil {
		http.Error(w, "Template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderFullPage renders the complete HTML page
func renderFullPage(w http.ResponseWriter, r *http.Request, path string) {
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
		parentPath := filepath.Dir(path)
		// For Windows compatibility, convert backslashes to forward slashes
		parentPath = filepath.ToSlash(parentPath)
		if parentPath == "." {
			parentPath = ""
		}

		// Get the parent directory info to use its actual timestamp
		var lastModified time.Time
		parentStat, err := os.Stat(parentPath)
		if err == nil {
			lastModified = parentStat.ModTime()
		} else {
			// If we can't stat the parent directory, use current time as fallback
			lastModified = time.Now()
			log.Printf("Warning: couldn't get info for parent directory %s: %v", parentPath, err)
		}

		// Create parent directory entry with proper timestamp
		parentInfo := FileInfo{
			Name:         "..",
			IsDir:        true,
			Path:         parentPath,
			LastModified: lastModified,
		}
		// Insert at the beginning
		files = append([]FileInfo{parentInfo}, files...)
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
