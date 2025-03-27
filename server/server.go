package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
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
	ListenAddr     string   // address to listen on for HTTP server
	Theme          string   // UI theme (light/dark)
	HideFooter     bool     // whether to hide the footer in the UI
	RootDir        string   // root directory to serve files from
	Version        string   // version information to display in UI
	Exclude        []string // patterns of files/directories to exclude
	Auth           string   // password for basic authentication
	Title          string   // custom title for the site
	SFTPUser       string   // username for SFTP authentication
	SFTPAddress    string   // address to listen for SFTP connections
	SFTPKeyFile    string   // path to SSH private key file
	SFTPAuthorized string   // path to authorized_keys file for public key authentication
	BrandName      string   // company or organization name for branding
	BrandColor     string   // color for navbar and footer
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
	router.HandleFunc("GET /partials/file-modal", wb.handleFileModal) // handle modal content
	router.HandleFunc("GET /view/{path...}", wb.handleViewFile)       // handle file viewing
	router.HandleFunc("GET /assets/css/custom.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "css/custom.css")
	})
	router.HandleFunc("GET /assets/css/weblist-app.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "css/weblist-app.css")
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
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
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

// handleDirContents renders partial directory contents for HTMX requests
func (wb *Web) handleDirContents(w http.ResponseWriter, r *http.Request) {
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

	// parse templates from embedded filesystem
	tmpl, err := template.New("index.html").Funcs(template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
	}).ParseFS(content, "templates/index.html", "templates/file.html")
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

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
		cookie, err := r.Cookie("auth")
		if err == nil && cookie.Value == wb.Auth {
			isAuthenticated = true
		}
	}

	data := struct {
		Files           []FileInfo
		Path            string
		DisplayPath     string
		SortBy          string
		SortDir         string
		PathParts       []map[string]string
		Theme           string
		Title           string
		IsAuthenticated bool
		BrandName       string
		BrandColor      string
	}{
		Files:           fileList,
		Path:            path,
		DisplayPath:     displayPath,
		SortBy:          sortBy,
		SortDir:         sortDir,
		PathParts:       wb.getPathParts(path, sortBy, sortDir),
		Theme:           wb.Theme,
		BrandName:       wb.BrandName,
		BrandColor:      wb.BrandColor,
		Title:           wb.Title,
		IsAuthenticated: isAuthenticated,
	}

	// execute just the page-content template
	if err := tmpl.ExecuteTemplate(w, "page-content", data); err != nil {
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
		http.Error(w, fmt.Sprintf("access denied: %s", filepath.Base(filePath)), http.StatusForbidden)
		return
	}

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("file not found: %s - %v", filepath.Base(filePath), err), http.StatusNotFound)
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
	contentType, isTextFile, isHTMLFile, _, _ := determineContentType(filePath)

	// for text files, check if the request wants dark mode
	isDarkMode := r.URL.Query().Get("theme") == "dark"

	if isTextFile {
		// read file content
		fileContent, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "error reading file", http.StatusInternalServerError)
			return
		}

		// use template for viewing
		w.Header().Set("Content-Type", "text/html")

		// determine theme based on query param
		theme := "light"
		if isDarkMode {
			theme = "dark"
		}

		// parse templates
		tmpl, err := wb.parseFileTemplates()
		if err != nil {
			log.Printf("[ERROR] failed to parse view template: %v", err)
			http.Error(w, "error rendering file view", http.StatusInternalServerError)
			return
		}

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
			IsHTML:   isHTMLFile,
		}

		// execute the file-view template
		if err := tmpl.ExecuteTemplate(w, "file-view", data); err != nil {
			log.Printf("[ERROR] failed to execute file-view template: %v", err)
			http.Error(w, "error rendering file view", http.StatusInternalServerError)
		}
	} else {
		// for non-text files (images, PDFs, etc.)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

		// serve the content directly
		http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file.(io.ReadSeeker))
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
		http.Error(w, fmt.Sprintf("access denied: %s", filepath.Base(filePath)), http.StatusForbidden)
		return
	}

	// check if the file exists and is not a directory
	fileInfo, err := fs.Stat(wb.FS, filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("file not found: %s", filepath.Base(filePath)), http.StatusNotFound)
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

// handleLoginPage renders the login page
func (wb *Web) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	tmpl, err := template.New("login.html").Funcs(template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
	}).ParseFS(content, "templates/login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Theme      string
		HideFooter bool
		Title      string
		Error      string
		BrandName  string
		BrandColor string
	}{
		Theme:      wb.Theme,
		HideFooter: wb.HideFooter,
		Title:      wb.Title,
		BrandName:  wb.BrandName,
		BrandColor: wb.BrandColor,
		Error:      "", // empty error by default
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
		tmpl, err := template.New("login.html").Funcs(template.FuncMap{
			"safe": func(s string) template.HTML {
				return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
			},
		}).ParseFS(content, "templates/login.html")
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
			return
		}

		data := struct {
			Theme      string
			HideFooter bool
			Title      string
			Error      string
			BrandName  string
			BrandColor string
		}{
			Theme:      wb.Theme,
			HideFooter: wb.HideFooter,
			Title:      wb.Title,
			BrandName:  wb.BrandName,
			BrandColor: wb.BrandColor,
			Error:      "Invalid username or password",
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
		http.Error(w, fmt.Sprintf("file not found: %s - %v", path, err), http.StatusNotFound)
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
	contentType, isText, isHTML, isPDF, isImage := determineContentType(path)

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
		ContentType: contentType,
		FileSize:    fileInfo.Size(),
		IsImage:     isImage,
		IsPDF:       isPDF,
		IsText:      isText,
		IsHTML:      isHTML,
		Theme:       wb.Theme,
	}

	// parse templates
	tmpl, err := wb.parseFileTemplates()
	if err != nil {
		log.Printf("[ERROR] failed to parse file-modal template: %v", err)
		http.Error(w, "error rendering file modal", http.StatusInternalServerError)
		return
	}

	// set content type and execute the file-modal template
	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.ExecuteTemplate(w, "file-modal", data); err != nil {
		log.Printf("[ERROR] failed to execute file-modal template: %v", err)
		http.Error(w, "error rendering file modal", http.StatusInternalServerError)
	}
}

// parseFileTemplates parses templates needed for file viewing and modal display
func (wb *Web) parseFileTemplates() (*template.Template, error) {
	return template.New("index.html").Funcs(template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
	}).ParseFS(content, "templates/index.html", "templates/file.html")
}

// getCommonTextExtensions returns a map of common text file extensions
func getCommonTextExtensions() map[string]bool {
	return map[string]bool{
		".yml":      true,
		".yaml":     true,
		".toml":     true,
		".ini":      true,
		".conf":     true,
		".config":   true,
		".md":       true,
		".markdown": true,
		".env":      true,
		".lock":     true,
		".go":       true,
		".py":       true,
		".js":       true,
		".ts":       true,
		".jsx":      true,
		".tsx":      true,
		".sh":       true,
		".bash":     true,
		".zsh":      true,
		".log":      true,
	}
}

// determineContentType determines content type and related flags for a file
func determineContentType(filePath string) (contentType string, isText, isHTML, isPDF, isImage bool) {
	ext := filepath.Ext(filePath)
	extLower := strings.ToLower(ext)
	commonTextExtensions := getCommonTextExtensions()

	if commonTextExtensions[extLower] {
		contentType = "text/plain"
	} else {
		contentType = mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "text/plain"
		}
	}

	isText = strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/xml") ||
		strings.Contains(contentType, "html") ||
		commonTextExtensions[extLower]
	isHTML = strings.Contains(contentType, "html")
	isPDF = contentType == "application/pdf"
	isImage = strings.HasPrefix(contentType, "image/")

	return contentType, isText, isHTML, isPDF, isImage
}

// getSortParams retrieves sort parameters from query or cookies and returns them
// it also sets cookies if query parameters are provided
func (wb *Web) getSortParams(w http.ResponseWriter, r *http.Request) (sortBy, sortDir string) {
	// check query parameters first
	sortBy = r.URL.Query().Get("sort")
	sortDir = r.URL.Query().Get("dir")

	// if sort parameters are provided in the query, use and save them to cookies
	if sortBy != "" || sortDir != "" {
		// if either is set, ensure both have values
		if sortBy == "" {
			sortBy = "name" // default sort
		}
		if sortDir == "" {
			sortDir = "asc" // default direction
		}

		// set cookies with sorting preferences
		http.SetCookie(w, &http.Cookie{
			Name:     "sortBy",
			Value:    sortBy,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil,
			MaxAge:   60 * 60 * 24 * 365, // 1 year
		})

		http.SetCookie(w, &http.Cookie{
			Name:     "sortDir",
			Value:    sortDir,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil,
			MaxAge:   60 * 60 * 24 * 365, // 1 year
		})
	} else {
		// if no sort parameters in query, try to get from cookies
		if sortByCookie, err := r.Cookie("sortBy"); err == nil {
			sortBy = sortByCookie.Value
		}
		if sortDirCookie, err := r.Cookie("sortDir"); err == nil {
			sortDir = sortDirCookie.Value
		}
	}

	// if still empty after checking cookies, use defaults
	if sortBy == "" {
		sortBy = "name" // default sort
	}
	if sortDir == "" {
		sortDir = "asc" // default direction
	}

	return sortBy, sortDir
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
	tmpl, err := template.New("index.html").Funcs(template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
	}).ParseFS(content, "templates/index.html", "templates/file.html")
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// get sort parameters from query or cookies
	sortBy, sortDir := wb.getSortParams(w, r)

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

	data := struct {
		Files           []FileInfo
		Path            string
		DisplayPath     string
		SortBy          string
		SortDir         string
		PathParts       []map[string]string
		Theme           string
		HideFooter      bool
		IsAuthenticated bool
		Title           string
		BrandName       string
		BrandColor      string
	}{
		Files:           fileList,
		Path:            path,
		DisplayPath:     displayPath,
		SortBy:          sortBy,
		SortDir:         sortDir,
		PathParts:       wb.getPathParts(path, sortBy, sortDir),
		Theme:           wb.Theme,
		HideFooter:      wb.HideFooter,
		IsAuthenticated: isAuthenticated,
		Title:           wb.Title,
		BrandName:       wb.BrandName,
		BrandColor:      wb.BrandColor,
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
	if len(wb.Exclude) == 0 {
		return false
	}

	// normalize path for matching
	normalizedPath := filepath.ToSlash(path)

	for _, pattern := range wb.Exclude {
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

		// if both are directories and we're sorting by size,
		// sort them by name in ascending order for predictability
		if files[i].IsDir && files[j].IsDir && sortBy == "size" {
			return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		}

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
