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
	Exclude    []string
	Auth       string // password for basic authentication
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

	// add authentication middleware if Auth is set
	if wb.Auth != "" {
		router.HandleFunc("GET /login", wb.handleLoginPage)
		router.HandleFunc("POST /login", wb.handleLoginSubmit)
		router.HandleFunc("GET /logout", wb.handleLogout)
		router.Use(wb.authMiddleware)
	}

	router.HandleFunc("GET /", wb.handleRoot)
	router.HandleFunc("GET /partials/dir-contents", wb.handleDirContents)
	router.HandleFunc("GET /assets/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "css/style.css")
	})
	router.HandleFunc("GET /assets/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "favicon.ico")
	})
	router.HandleFunc("GET /{path...}", wb.handleDownload) // handle file downloads with just the path

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
		http.Error(w, fmt.Sprintf("access denied: %s", filepath.Base(filePath)), http.StatusForbidden)
		return
	}

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

	// parse template
	tmpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// use the helper function to prepare data
	data, err := wb.prepareDirectoryData(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// add authentication info
	if wb.Auth != "" {
		cookie, err := r.Cookie("auth")
		if err == nil && cookie.Value == wb.Auth {
			data["IsAuthenticated"] = true
		}
	}

	// execute just the page-content template
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
		http.Redirect(w, r, "/"+path, http.StatusSeeOther)
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

	// check if user is authenticated (for showing logout button)
	isAuthenticated := false
	if wb.Auth != "" {
		cookie, err := r.Cookie("auth")
		if err == nil && cookie.Value == wb.Auth {
			isAuthenticated = true
		}
	}

	data := map[string]any{
		"Files":           fileList,
		"Path":            path,
		"DisplayPath":     displayPath,
		"SortBy":          sortBy,
		"SortDir":         sortDir,
		"PathParts":       wb.getPathParts(path, sortBy, sortDir),
		"Theme":           wb.Config.Theme,
		"HideFooter":      wb.Config.HideFooter,
		"IsAuthenticated": isAuthenticated,
	}

	// execute the entire template
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// getFileList returns a list of files in the specified directory
func (wb *Web) getFileList(path, sortBy, sortDir string) ([]FileInfo, error) {
	// get the list of files in the directory
	entries, err := fs.ReadDir(wb.FS, path)
	if err != nil {
		return nil, err
	}

	// convert the entries to FileInfo
	files := make([]FileInfo, 0, len(entries))

	// add a parent directory entry if we're not at the root
	if path != "." {
		parentPath := filepath.Dir(path)

		// create parent directory entry with minimal information
		parentEntry := FileInfo{
			Name:  "..",
			IsDir: true,
			Path:  parentPath,
			// LastModified intentionally omitted - will be zero value
			// this is better than showing incorrect time
		}

		// try to get the actual modification time of the parent directory
		// but don't fail if we can't - just leave LastModified as zero value
		if parentInfo, err := fs.Stat(wb.FS, parentPath); err == nil && parentInfo != nil {
			parentEntry.LastModified = parentInfo.ModTime()
		}

		files = append(files, parentEntry)
	}

	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())

		// skip excluded files and directories
		if wb.shouldExclude(entryPath) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("[WARN] failed to get info for %s: %v", entry.Name(), err)
			continue
		}

		files = append(files, FileInfo{
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime(),
			IsDir:        entry.IsDir(),
			Path:         entryPath,
		})
	}

	// sort the file list
	wb.sortFiles(files, sortBy, sortDir)

	return files, nil
}

// shouldExclude checks if a path should be excluded based on the Exclude patterns
func (wb *Web) shouldExclude(path string) bool {
	if len(wb.Config.Exclude) == 0 {
		return false
	}

	// normalize path for matching
	normalizedPath := filepath.ToSlash(path)

	for _, pattern := range wb.Config.Exclude {
		// convert pattern to use forward slashes for consistency
		pattern = filepath.ToSlash(pattern)

		// check if the path matches the pattern exactly
		if normalizedPath == pattern {
			return true
		}

		// check if the path contains the pattern as a directory component
		// this handles cases like "some/git/path" when pattern is ".git"
		parts := strings.Split(normalizedPath, "/")
		for _, part := range parts {
			if part == pattern {
				return true
			}
		}

		// check if the path ends with the pattern
		if strings.HasSuffix(normalizedPath, "/"+pattern) {
			return true
		}
	}

	return false
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

// authMiddleware checks if the user is authenticated
func (wb *Web) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// skip authentication for login page and assets
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}

		// check if user is authenticated via cookie
		cookie, err := r.Cookie("auth")
		if err == nil && cookie.Value == wb.Auth {
			// user is authenticated, proceed
			next.ServeHTTP(w, r)
			return
		}

		// check if user is authenticated via basic auth
		username, password, ok := r.BasicAuth()
		if ok && username == "weblist" && password == wb.Auth {
			// set cookie for future requests
			http.SetCookie(w, &http.Cookie{
				Name:     "auth",
				Value:    wb.Auth,
				Path:     "/",
				HttpOnly: true,
				Secure:   r.TLS != nil,
				MaxAge:   3600 * 24, // 24 hours
			})
			next.ServeHTTP(w, r)
			return
		}

		// user is not authenticated, redirect to login page
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// handleLoginPage renders the login page
func (wb *Web) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	tmpl, err := template.ParseFS(content, "templates/login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Theme":      wb.Theme,
		"HideFooter": wb.HideFooter,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("failed to execute template: %v", err), http.StatusInternalServerError)
		return
	}
}

// handleLoginSubmit handles the login form submission
func (wb *Web) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username != "weblist" || password != wb.Auth {
		// authentication failed, show error
		tmpl, err := template.ParseFS(content, "templates/login.html")
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
			return
		}

		data := map[string]interface{}{
			"Theme":      wb.Theme,
			"HideFooter": wb.HideFooter,
			"Error":      "Invalid username or password",
		}

		if err := tmpl.Execute(w, data); err != nil {
			http.Error(w, fmt.Sprintf("failed to execute template: %v", err), http.StatusInternalServerError)
			return
		}
		return
	}

	// authentication successful, set cookie and redirect
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    wb.Auth,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		MaxAge:   3600 * 24, // 24 hours
	})

	// redirect to the home page
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout handles the logout request
func (wb *Web) handleLogout(w http.ResponseWriter, r *http.Request) {
	// clear the auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		MaxAge:   -1, // delete the cookie
	})

	// redirect to the login page
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// prepareDirectoryData prepares the data for directory rendering
func (wb *Web) prepareDirectoryData(path, sortBy, sortDir string) (map[string]interface{}, error) {
	// clean the path to avoid directory traversal
	path = filepath.ToSlash(filepath.Clean(path))

	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %w", err)
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	return map[string]interface{}{
		"Files":       fileList,
		"Path":        path,
		"DisplayPath": displayPath,
		"SortBy":      sortBy,
		"SortDir":     sortDir,
		"PathParts":   wb.getPathParts(path, sortBy, sortDir),
		"Theme":       wb.Config.Theme,
	}, nil
}
