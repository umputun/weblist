package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/didip/tollbooth/v8"
	"github.com/didip/tollbooth/v8/limiter"
	"github.com/go-pkgz/lcw/v2"
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

	// cached templates
	templates struct {
		initialized   bool
		indexTemplate *template.Template
		fileTemplate  *template.Template
		loginTemplate *template.Template
	}

	binaryCache lcw.LoadingCache[bool] // caches binary detection results by path+mtime
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
	AuthUser                 string        // username for basic authentication (defaults to "weblist")
	SessionSecret            string        // secret key for signing session tokens
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
	EnableMultiSelect        bool          // enable multi-file selection and download
	RecursiveMtime           bool          // calculate directory mtime from newest nested file
	EnableUpload             bool          // enable file upload support
	UploadMaxSize            int64         // max upload size in bytes
	UploadOverwrite          bool          // allow overwriting existing files on upload
}

// Run starts the web server.
func (wb *Web) Run(ctx context.Context) error {
	// validate listen address
	if wb.ListenAddr == "" {
		return fmt.Errorf("listen address cannot be empty")
	}

	// normalize brand color if provided
	wb.BrandColor = wb.normalizeBrandColor(wb.BrandColor)

	// initialize SessionSecret if not provided to avoid race conditions
	if wb.SessionSecret == "" {
		randomSecret := make([]byte, 32)
		if _, err := rand.Read(randomSecret); err != nil {
			log.Printf("[WARN] failed to generate random session secret: %v", err)
			wb.SessionSecret = uuid.NewString()
		} else {
			wb.SessionSecret = base64.StdEncoding.EncodeToString(randomSecret)
		}
		log.Printf("[INFO] generated random session secret during startup")
	}

	// initialize binary detection cache
	if wb.binaryCache == nil {
		var cacheErr error
		wb.binaryCache, cacheErr = lcw.NewLruCache(lcw.NewOpts[bool]().MaxKeys(10000))
		if cacheErr != nil {
			return fmt.Errorf("failed to create binary cache: %w", cacheErr)
		}
	}

	router, err := wb.router()
	if err != nil {
		return fmt.Errorf("failed to create router: %w", err)
	}

	readTimeout := 10 * time.Second
	writeTimeout := 30 * time.Second
	if wb.EnableUpload {
		readTimeout = 5 * time.Minute
		writeTimeout = 5 * time.Minute
	}

	srv := &http.Server{
		Addr:              wb.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
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

// initTemplates initializes and caches the HTML templates
func (wb *Web) initTemplates() error {
	if wb.templates.initialized {
		return nil
	}

	// get template functions
	funcMap := wb.getTemplateFuncs()

	// define the template files
	templateFiles := []string{
		"templates/index.html",
		"templates/file.html",
		"templates/selection-status.html",
	}

	// parse index template
	indexTemplate, err := template.New("index.html").Funcs(funcMap).ParseFS(content, templateFiles...)
	if err != nil {
		return fmt.Errorf("failed to parse index template: %w", err)
	}
	wb.templates.indexTemplate = indexTemplate

	// parse file template (reusing index.html which includes file.html)
	fileTemplate, err := template.New("index.html").Funcs(funcMap).ParseFS(content, templateFiles...)
	if err != nil {
		return fmt.Errorf("failed to parse file template: %w", err)
	}
	wb.templates.fileTemplate = fileTemplate

	// parse login template
	loginTemplate, err := template.New("login.html").Funcs(funcMap).ParseFS(content, "templates/login.html")
	if err != nil {
		return fmt.Errorf("failed to parse login template: %w", err)
	}
	wb.templates.loginTemplate = loginTemplate

	wb.templates.initialized = true
	return nil
}

// router creates a new router for the web server, configures middleware, and sets up routes.
func (wb *Web) router() (http.Handler, error) {
	// initialize templates
	if err := wb.initTemplates(); err != nil {
		return nil, fmt.Errorf("failed to initialize templates: %w", err)
	}
	// create router and set up routes
	mux := http.NewServeMux()
	router := routegroup.New(mux)

	router.Use(rest.Trace, rest.RealIP, rest.Recoverer(lgr.Default()))
	router.Use(rest.Throttle(1000))

	// global rate limiter - 50 requests per second
	router.Use(tollbooth.HTTPMiddleware(tollbooth.NewLimiter(50, nil)))

	// create a more restrictive rate limiter for authentication endpoints
	authLimiter := tollbooth.NewLimiter(5, nil)
	authLimiter.SetIPLookup(limiter.IPLookup{Name: "RemoteAddr"})
	authLimiter.SetBurst(5) // allow burst of 5 requests
	authLimiter.SetMessage("Too many login attempts, please try again later")
	authLimiter.SetTokenBucketExpirationTTL(10 * time.Minute) // reset after 10 minutes

	router.Use(logger.New(logger.Log(lgr.Default()), logger.Prefix("[DEBUG]")).Handler)
	router.Use(rest.AppInfo("weblist", "umputun", wb.Version), rest.Ping)
	router.Use(wb.securityHeadersMiddleware) // add security headers to all responses

	// serve static assets from embedded filesystem
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, fmt.Errorf("failed to load embedded assets: %w", err)
	}

	// register upload route in its own group without SizeLimit, so large uploads are allowed.
	// the upload handler applies its own MaxBytesReader with UploadMaxSize.
	if wb.EnableUpload {
		router.Group().Route(func(uploadGroup *routegroup.Bundle) {
			if wb.Auth != "" {
				uploadGroup.Use(wb.authMiddleware)
			}
			uploadGroup.HandleFunc("POST /upload", wb.handleUpload)
		})
	}

	// main route group with SizeLimit for all existing routes (including login)
	router.Group().Route(func(main *routegroup.Bundle) {
		main.Use(rest.SizeLimit(1024 * 1024)) // 1M max request size

		// add authentication routes if Auth is set
		if wb.Auth != "" {
			main.HandleFunc("GET /login", wb.handleLoginPage)

			// apply the stricter rate limiter to login submission endpoint
			loginHandler := tollbooth.LimitFuncHandler(authLimiter, wb.handleLoginSubmit)
			main.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
				loginHandler.ServeHTTP(w, r)
			})

			main.HandleFunc("GET /logout", wb.handleLogout)
		}

		main.Group().Route(func(auth *routegroup.Bundle) {
			if wb.Auth != "" {
				auth.Use(wb.authMiddleware)
			}
			auth.HandleFunc("GET /", wb.handleRoot)
			auth.HandleFunc("GET /partials/dir-contents", wb.handleDirContents)
			auth.HandleFunc("GET /partials/file-modal", wb.handleFileModal)              // handle modal content
			auth.HandleFunc("POST /partials/selection-status", wb.handleSelectionStatus) // handle selection update
			auth.HandleFunc("POST /download-selected", wb.handleDownloadSelected)        // handle multi-file download
			auth.HandleFunc("GET /view/{path...}", wb.handleViewFile)                    // handle file viewing
			auth.HandleFunc("GET /api/list", wb.handleAPIList)                           // handle JSON API for file listing
			auth.HandleFunc("GET /{path...}", wb.handleDownload)                         // handle file downloads with just the path
		})
	})

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

	// serve favicon.ico at root level without auth, browsers request this path conventionally
	router.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "favicon.png")
	})

	return router, nil
}

// authMiddleware enforces authentication for protected routes.
// It uses a multi-tiered authentication approach:
// 1. Login page (/login) and static assets are always accessible without authentication
// 2. Checks for a valid authentication cookie first
// 3. Falls back to HTTP Basic Auth with username "weblist" and password from config
// 4. On successful Basic Auth, sets a cookie for future requests to avoid repeated authentication
// 5. Redirects unauthenticated requests to the login page
// This middleware belongs after all other middleware but before route handlers.
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

// securityHeadersMiddleware adds security-related HTTP headers to all responses
func (wb *Web) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// prevent MIME type sniffing (which can lead to XSS)
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// allow framing from same origin
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")

		// enable browser XSS filtering
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// strict MIME type checking
		csp := []string{
			"default-src 'self'",
			"img-src 'self' data:",
			"style-src 'self' 'unsafe-inline'",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"font-src 'self'",
		}
		w.Header().Set("Content-Security-Policy", strings.Join(csp, "; "))

		// prevent browsers from identifying this application as a web application
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")

		// prevent search engines from indexing (optional, remove if public)
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")

		// serve the request
		next.ServeHTTP(w, r)
	})
}

// isHTMXRequest checks if the request is from HTMX by examining the HX-Request header
func (wb *Web) isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
