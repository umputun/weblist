package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
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

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/didip/tollbooth/v8"
	"github.com/go-pkgz/lgr"
	"github.com/go-pkgz/rest"
	"github.com/go-pkgz/rest/logger"
	"github.com/go-pkgz/routegroup"
	"github.com/google/uuid"
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
	ListenAddr               string        // address to listen on for HTTP server
	Theme                    string        // UI theme (light/dark)
	HideFooter               bool          // whether to hide the footer in the UI
	RootDir                  string        // root directory to serve files from
	Version                  string        // version information to display in UI
	Exclude                  []string      // patterns of files/directories to exclude
	Auth                     string        // password for basic authentication
	Title                    string        // custom title for the site
	SFTPUser                 string        // username for SFTP authentication
	SFTPAddress              string        // address to listen for SFTP connections
	SFTPKeyFile              string        // path to SSH private key file
	SFTPAuthorized           string        // path to authorized_keys file for public key authentication
	BrandName                string        // company or organization name for branding
	BrandColor               string        // color for navbar
	EnableSyntaxHighlighting bool          // whether to enable syntax highlighting for code files
	CustomFooter             string        // custom footer text (can contain HTML)
	InsecureCookies          bool          // allow cookies without secure flag
	SessionTTL               time.Duration // session timeout duration
}

// Run starts the web server.
func (wb *Web) Run(ctx context.Context) error {
	// normalize brand color if provided
	wb.BrandColor = wb.normalizeBrandColor(wb.BrandColor)

	router, err := wb.router()
	if err != nil {
		return fmt.Errorf("failed to create router: %w", err)
	}

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
		// create a new derived context with timeout for shutdown
		// we can't use the parent context as it may already be canceled
		baseCtx := context.Background() //nolint:contextcheck // we need a fresh context since parent may be canceled
		if ctx.Err() == nil {
			baseCtx = ctx
		}
		shutdownCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("[INFO] server shutdown completed with context canceled")
				return nil
			}
			return fmt.Errorf("[WARN] graceful shutdown failed: %w", err)
		}
		log.Printf("[INFO] server shutdown completed")
		return nil
	}
}

// router creates a new router for the web server, configures middleware, and sets up routes.
func (wb *Web) router() (http.Handler, error) {
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
		return nil, fmt.Errorf("failed to load embedded assets: %w", err)
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
	router.HandleFunc("GET /api/list", wb.handleAPIList)              // handle JSON API for file listing

	// handler for all static assets
	router.HandleFunc("GET /assets/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/assets/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		if path == "favicon.ico" { // special case for favicon.ico which maps to favicon.png
			path = "favicon.png"
		}
		http.ServeFileFS(w, r, assetsFS, path)
	})
	router.HandleFunc("GET /{path...}", wb.handleDownload) // handle file downloads with just the path

	return router, nil
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
	tmpl, err := template.New("index.html").Funcs(wb.getTemplateFuncs()).ParseFS(content, "templates/index.html", "templates/file.html")
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
		if err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(wb.Auth)) == 1 {
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
		CustomFooter    string
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
		CustomFooter:    wb.CustomFooter,
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
	contentType, isTextFile, isHTMLFile, _, _ := DetermineContentType(filePath)

	// handle non-text files (images, PDFs, etc.)
	if !isTextFile {
		w.Header().Set("Content-Type", contentType)
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

	// determine theme based on query param
	isDarkMode := r.URL.Query().Get("theme") == "dark"
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

	// apply syntax highlighting for non-HTML text files if enabled
	if !isHTMLFile && wb.EnableSyntaxHighlighting {
		highlighted, err := wb.highlightCode(string(fileContent), fileInfo.Name(), theme)
		if err != nil {
			log.Printf("[WARN] failed to highlight code: %v", err)
			// fall back to plain text if highlighting fails
		} else {
			data.Content = highlighted
		}
	}

	// execute the file-view template
	if err := tmpl.ExecuteTemplate(w, "file-view", data); err != nil {
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
func (wb *Web) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("login.html").Funcs(wb.getTemplateFuncs()).ParseFS(content, "templates/login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	// generate CSRF token
	csrfToken := wb.generateCSRFToken()

	// set CSRF token in a cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(5 * time.Minute.Seconds()), // CSRF token valid for 5 minutes
	})

	data := struct {
		Theme        string
		HideFooter   bool
		Title        string
		Error        string
		BrandName    string
		BrandColor   string
		CustomFooter string
		CSRFToken    string
	}{
		Theme:        wb.Theme,
		HideFooter:   wb.HideFooter,
		Title:        wb.Title,
		BrandName:    wb.BrandName,
		BrandColor:   wb.BrandColor,
		Error:        "", // empty error by default
		CustomFooter: wb.CustomFooter,
		CSRFToken:    csrfToken,
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

	// verify CSRF token
	formToken := r.FormValue("csrf_token")
	cookieToken, err := r.Cookie("csrf_token")
	if err != nil || formToken == "" || subtle.ConstantTimeCompare([]byte(formToken), []byte(cookieToken.Value)) != 1 {
		wb.renderLoginError(w, r, "Invalid or missing CSRF token")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// check credentials
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte("weblist")) != 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(wb.Auth)) != 1

	// authentication failed, show error
	if usernameMatch || passwordMatch {
		wb.renderLoginError(w, r, "Invalid username or password")
		return
	}

	// clear the CSRF token cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   -1, // delete the cookie
	})

	// authentication successful, set session cookie
	maxAge := int(wb.SessionTTL.Seconds())
	if maxAge == 0 {
		maxAge = 3600 * 24 // default to 24 hours if not specified
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    wb.Auth,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})

	// redirect to the home page
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderLoginError renders the login page with an error message
func (wb *Web) renderLoginError(w http.ResponseWriter, r *http.Request, errorMsg string) {
	tmpl, err := template.New("login.html").Funcs(wb.getTemplateFuncs()).ParseFS(content, "templates/login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	// generate a new CSRF token
	csrfToken := wb.generateCSRFToken()

	// set CSRF token in cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(5 * time.Minute.Seconds()),
	})

	data := struct {
		Theme        string
		HideFooter   bool
		Title        string
		Error        string
		BrandName    string
		BrandColor   string
		CustomFooter string
		CSRFToken    string
	}{
		Theme:        wb.Theme,
		HideFooter:   wb.HideFooter,
		Title:        wb.Title,
		BrandName:    wb.BrandName,
		BrandColor:   wb.BrandColor,
		Error:        errorMsg,
		CustomFooter: wb.CustomFooter,
		CSRFToken:    csrfToken,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("failed to execute template: %v", err), http.StatusInternalServerError)
	}
}

// handleLogout handles the logout request
func (wb *Web) handleLogout(w http.ResponseWriter, r *http.Request) {
	// clear the auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
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
	contentType, isText, isHTML, isPDF, isImage := DetermineContentType(path)

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

// getTemplateFuncs returns the common template functions map
func (wb *Web) getTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
		"contains": strings.Contains,
	}
}

// parseFileTemplates parses templates needed for file viewing and modal display
func (wb *Web) parseFileTemplates() (*template.Template, error) {
	return template.New("index.html").Funcs(wb.getTemplateFuncs()).ParseFS(content, "templates/index.html", "templates/file.html")
}

// getSortParams retrieves sort parameters from query or cookies and returns them
// it also sets cookies if query parameters are provided
func (wb *Web) getSortParams(w http.ResponseWriter, r *http.Request) (sortBy, sortDir string) {
	// check query parameters first
	sortBy = r.URL.Query().Get("sort")
	sortDir = r.URL.Query().Get("dir")

	// handle parameters from query
	if sortBy != "" || sortDir != "" {
		return wb.processSortQueryParams(w, r, sortBy, sortDir)
	}

	// handle parameters from cookies
	return wb.getSortParamsFromCookies(r)
}

// processSortQueryParams processes and saves sort parameters from query
func (wb *Web) processSortQueryParams(w http.ResponseWriter, r *http.Request, sortBy, sortDir string) (resultSortBy, resultSortDir string) {
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
		Secure:   wb.isRequestSecure(r),
		MaxAge:   60 * 60 * 24 * 365, // 1 year
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "sortDir",
		Value:    sortDir,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   60 * 60 * 24 * 365, // 1 year
	})

	return sortBy, sortDir
}

// getSortParamsFromCookies gets sort parameters from cookies with defaults
func (wb *Web) getSortParamsFromCookies(r *http.Request) (sortBy, sortDir string) {
	// try to get from cookies
	if sortByCookie, err := r.Cookie("sortBy"); err == nil {
		sortBy = sortByCookie.Value
	}
	if sortDirCookie, err := r.Cookie("sortDir"); err == nil {
		sortDir = sortDirCookie.Value
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
	tmpl, err := template.New("index.html").Funcs(wb.getTemplateFuncs()).ParseFS(content, "templates/index.html", "templates/file.html")
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
		if err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(wb.Auth)) == 1 {
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
		CustomFooter    string
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
		CustomFooter:    wb.CustomFooter,
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

// shouldExclude checks if a path should be excluded based on the Exclude patterns.
// It performs several matching strategies:
// 1. Exact path match with the pattern
// 2. Any directory component matches the pattern (e.g. ".git" would exclude ".git/config")
// 3. Path ends with the pattern as a directory component
// All paths are normalized to use forward slashes for consistent matching across platforms.
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

// sortFiles sorts the file list based on the specified criteria (name, date, or size).
// The sort maintains several important properties:
// 1. The ".." parent directory entry always appears first
// 2. Directories are always grouped before files, regardless of sort field
// 3. When directories are sorted by size, they're sorted by name instead for consistency
// 4. Files are sorted by the requested field with case-insensitive name comparison
// 5. The sortDir parameter (asc/desc) reverses the sort order when set to "desc"
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
			if files[i].IsDir && files[j].IsDir {
				// if both are directories, sort by name in ascending order regardless of sortDir
				return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
			}
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

// authMiddleware enforces authentication for protected routes.
// It uses a multi-tiered authentication approach:
// 1. Login page (/login) and static assets are always accessible without authentication
// 2. Checks for a valid authentication cookie first
// 3. Falls back to HTTP Basic Auth with username "weblist" and password from config
// 4. On successful Basic Auth, sets a cookie for future requests to avoid repeated authentication
// 5. Redirects unauthenticated requests to the login page
// This middleware should be added after all other middleware but before route handlers.
func (wb *Web) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// skip authentication for login page and assets
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}

		// check if user is authenticated via cookie
		if wb.isAuthenticatedByCookie(r) {
			next.ServeHTTP(w, r)
			return
		}

		// check if user is authenticated via basic auth
		if wb.tryBasicAuth(w, r) {
			next.ServeHTTP(w, r)
			return
		}

		// user is not authenticated, redirect to login page
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// isAuthenticatedByCookie checks if the user is authenticated via cookie
func (wb *Web) isAuthenticatedByCookie(r *http.Request) bool {
	cookie, err := r.Cookie("auth")
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(wb.Auth)) == 1
}

// tryBasicAuth checks if the user is authenticated via basic auth
// and sets a cookie on success
func (wb *Web) tryBasicAuth(w http.ResponseWriter, r *http.Request) bool {
	username, password, ok := r.BasicAuth()

	// if basic auth is not provided or invalid
	if !ok {
		return false
	}

	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte("weblist")) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(wb.Auth)) == 1

	// if credentials don't match
	if !usernameMatch || !passwordMatch {
		return false
	}

	// set cookie for future requests
	maxAge := int(wb.SessionTTL.Seconds())
	if maxAge == 0 {
		maxAge = 3600 * 24 // default to 24 hours if not specified
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    wb.Auth,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})

	return true
}

// normalizeBrandColor ensures the brand color has a # prefix if it's a hex color
func (wb *Web) normalizeBrandColor(color string) string {
	if color == "" {
		return ""
	}

	// if color doesn't start with #, add it (assuming it's a hex color)
	if !strings.HasPrefix(color, "#") {
		return "#" + color
	}

	return color
}

// generateCSRFToken creates a random token for CSRF protection
func (wb *Web) generateCSRFToken() string {
	const tokenLength = 32
	b := make([]byte, tokenLength)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		// if crypto/rand fails, use uuid which has its own entropy source
		log.Printf("[WARN] Failed to generate random CSRF token: %v, using UUID fallback", err)
		return uuid.NewString()
	}
	return fmt.Sprintf("%x", b)
}

// isRequestSecure checks if the request is secure by examining TLS status and common proxy headers
func (wb *Web) isRequestSecure(r *http.Request) bool {
	// if insecure cookies is enabled, we don't care about the request security
	if wb.InsecureCookies {
		return false
	}

	// check if the connection itself is secure
	if r != nil && r.TLS != nil {
		return true
	}

	// check common proxy headers for HTTPS
	if r != nil {
		// x-Forwarded-Proto is the de-facto standard header for proxies
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			return true
		}
		// check Forwarded header (RFC 7239)
		if fwd := r.Header.Get("Forwarded"); fwd != "" {
			// RFC 7239 specifies that Forwarded header may contain multiple
			// comma-separated entries, each with semicolon-separated parameters
			for _, entry := range strings.Split(fwd, ",") {
				entry = strings.TrimSpace(entry)
				for _, part := range strings.Split(entry, ";") {
					part = strings.TrimSpace(part)
					if strings.HasPrefix(part, "proto=") && strings.ToLower(strings.TrimPrefix(part, "proto=")) == "https" {
						return true
					}
				}
			}
		}
	}

	return false
}

// highlightCode applies syntax highlighting to the given code content
func (wb *Web) highlightCode(code, filename, theme string) (string, error) {
	// get lexer for the file
	lexer := lexers.Get(filename)
	if lexer == nil {
		// try to detect language from content if filename doesn't help
		lexer = lexers.Analyse(code)
		if lexer == nil {
			// fall back to plain text if no lexer found
			return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), nil
		}
	}

	// get style based on theme
	var style *chroma.Style
	if theme == "dark" {
		style = styles.Get("monokai")
	} else {
		style = styles.Get("github")
	}

	// create HTML formatter with line numbers
	formatter := html.New(html.WithClasses(true))

	// write HTML header
	var buf strings.Builder
	buf.WriteString(`<div class="highlight-wrapper">`)

	// tokenize and format the code
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), err
	}

	// format the tokens
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), err
	}

	// write HTML footer
	buf.WriteString("</div>")

	return buf.String(), nil
}

// writeJSONError writes a JSON error response with the specified status code
func writeJSONError(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": errMsg}); err != nil {
		log.Printf("[ERROR] failed to encode error response: %v", err)
	}
}

// parseSortParams extracts and validates sort parameters from the request
func parseSortParams(sortParam string) (sortBy, sortDir string) {
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
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("directory not found: %v", err))
		return
	}

	if !fileInfo.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "not a directory")
		return
	}

	// parse the sort parameter
	sortParam := r.URL.Query().Get("sort")
	sortBy, sortDir := parseSortParams(sortParam)

	// get the file list
	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("error reading directory: %v", err))
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
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("error encoding JSON: %v", err))
	}
}
