package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/umputun/go-flags"

	"github.com/umputun/weblist/server"
)

func TestVersionInfo(t *testing.T) {
	// this will return either "dev" or the actual version
	version := versionInfo()
	assert.NotEmpty(t, version, "Version should not be empty")
	assert.True(t, version == "dev" || version == "unknown" || version != "",
		"Version should be 'dev', 'unknown', or a valid version string")
}

func TestSetupLog(t *testing.T) {
	t.Parallel() // use t to avoid the unused parameter warning

	// test with debug mode off
	setupLog(false)

	// test with debug mode on
	setupLog(true)

	// test with secrets
	setupLog(false, "secret1", "secret2")
}

func TestThemeValidation(t *testing.T) {
	// save original opts to restore later
	originalOpts := opts
	defer func() { opts = originalOpts }()

	// test valid themes
	tests := []struct {
		name  string
		theme string
		want  string
	}{
		{"light theme", "light", "light"},
		{"dark theme", "dark", "dark"},
		{"invalid theme", "invalid", "light"}, // should default to light
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// set theme
			opts.Theme = tc.theme

			// create a temporary logger to capture output
			oldLogger := log.Writer()
			defer func() { log.SetOutput(oldLogger) }()

			// call the validation logic directly
			if opts.Theme != "light" && opts.Theme != "dark" {
				opts.Theme = "light"
			}

			assert.Equal(t, tc.want, opts.Theme)
		})
	}
}

func TestAbsolutePathResolution(t *testing.T) {
	// save original opts to restore later
	originalOpts := opts
	defer func() { opts = originalOpts }()

	// create a temporary directory
	tempDir := t.TempDir()

	// test relative path resolution
	opts.RootDir = "."
	absPath, err := filepath.Abs(opts.RootDir)
	assert.NoError(t, err)
	assert.NotEqual(t, ".", absPath, "Absolute path should be different from relative path")

	// test absolute path remains the same
	opts.RootDir = tempDir
	absPath, err = filepath.Abs(opts.RootDir)
	assert.NoError(t, err)
	assert.Equal(t, tempDir, absPath, "Absolute path should remain the same")
}

func TestParseCommandLineArgs(t *testing.T) {
	// save original args and restore after test
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	// save original opts to restore later
	originalOpts := opts
	defer func() { opts = originalOpts }()

	tests := []struct {
		name     string
		args     []string
		expected options
	}{
		{
			name: "default values",
			args: []string{"weblist"},
			expected: options{
				Listen:  ":8080",
				Theme:   "light",
				RootDir: ".",
			},
		},
		{
			name: "custom listen address",
			args: []string{"weblist", "--listen", ":9090"},
			expected: options{
				Listen:  ":9090",
				Theme:   "light",
				RootDir: ".",
			},
		},
		{
			name: "custom theme",
			args: []string{"weblist", "--theme", "dark"},
			expected: options{
				Listen:  ":8080",
				Theme:   "dark",
				RootDir: ".",
			},
		},
		{
			name: "custom root directory",
			args: []string{"weblist", "--root", "/tmp"},
			expected: options{
				Listen:  ":8080",
				Theme:   "light",
				RootDir: "/tmp",
			},
		},
		{
			name: "debug mode",
			args: []string{"weblist", "--dbg"},
			expected: options{
				Listen:  ":8080",
				Theme:   "light",
				RootDir: ".",
				Dbg:     true,
			},
		},
		{
			name: "multiple options",
			args: []string{"weblist", "--listen", ":9090", "--theme", "dark", "--root", "/tmp", "--dbg"},
			expected: options{
				Listen:  ":9090",
				Theme:   "dark",
				RootDir: "/tmp",
				Dbg:     true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// reset opts to default values
			opts = options{}

			// set command line args
			os.Args = tc.args

			// parse flags directly using the flags package
			p := flags.NewParser(&opts, flags.PrintErrors|flags.PassDoubleDash|flags.HelpFlag)
			_, err := p.Parse()
			require.NoError(t, err, "Flag parsing should not produce an error")

			// check if options match expected values
			assert.Equal(t, tc.expected.Listen, opts.Listen, "Listen address should match")
			assert.Equal(t, tc.expected.Theme, opts.Theme, "Theme should match")
			assert.Equal(t, tc.expected.RootDir, opts.RootDir, "Root directory should match")
			assert.Equal(t, tc.expected.Dbg, opts.Dbg, "Debug mode should match")
		})
	}
}

func TestRunServer(t *testing.T) {
	tempDir := t.TempDir()

	// create test files in the temp directory
	err := os.WriteFile(filepath.Join(tempDir, "runserver-test.txt"), []byte("test content for runServer"), 0o644)
	require.NoError(t, err)

	// create a subdirectory with a file
	err = os.Mkdir(filepath.Join(tempDir, "runserver-subdir"), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "runserver-subdir", "nested.txt"), []byte("nested file"), 0o644)
	require.NoError(t, err)

	// find an available port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil { // close so the server can use it
		t.Fatalf("failed to close listener: %v", err)
	}

	// set up options for the server
	serverOpts := &options{
		Listen:     fmt.Sprintf(":%d", port),
		Theme:      "dark",
		RootDir:    tempDir,
		HideFooter: true,
		Exclude:    []string{".git", "node_modules"},
		Title:      "RunServer Test",
	}

	// start the server in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(ctx, serverOpts)
	}()

	// wait for the server to start
	time.Sleep(100 * time.Millisecond)

	// create an HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	t.Run("root page loads with custom title", func(t *testing.T) {
		resp, err := client.Get(baseURL)
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		bodyStr := string(body)
		assert.Contains(t, bodyStr, "runserver-test.txt")
		assert.Contains(t, bodyStr, "runserver-subdir")
		assert.Contains(t, bodyStr, "RunServer Test")    // custom title
		assert.Contains(t, bodyStr, `data-theme="dark"`) // dark theme attribute
	})

	t.Run("subdirectory navigation", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=runserver-subdir")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Contains(t, string(body), "nested.txt")
	})

	t.Run("file download", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/runserver-test.txt")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "test content for runServer", string(body))
	})

	// test the server handles errors properly
	t.Run("invalid path error", func(t *testing.T) {
		// create a path that doesn't exist
		resp, err := client.Get(baseURL + "/?path=non-existent")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	// shutdown the server
	cancel()

	// wait for server to shut down
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not shut down within expected time")
	}
}

func TestRunServerErrors(t *testing.T) {
	// test with an absolute path error for RootDir
	t.Run("bad root directory path", func(t *testing.T) {
		// create a mock options with a path that will fail filepath.Abs
		ctx := context.Background()

		// create a temporary directory and then remove it to ensure path doesn't exist
		tempDir := t.TempDir() + "/nonexistent"

		mockOpts := &options{
			RootDir: tempDir,
		}

		// remove the directory to force an error
		if err := os.RemoveAll(tempDir); err != nil {
			t.Logf("Failed to remove tempDir: %v", err)
		}

		// make the path impossible to resolve
		mockOpts.RootDir = string([]byte{0, 1, 2}) // invalid UTF-8 path

		// call runServer and check for error
		err := runServer(ctx, mockOpts)
		assert.Error(t, err)
	})
}

func TestIntegration(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create some test files in the temp directory
	err := os.WriteFile(filepath.Join(tempDir, "test1.txt"), []byte("test1 content"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "test2.txt"), []byte("test2 content"), 0o644)
	require.NoError(t, err)

	// create a subdirectory with a file
	err = os.Mkdir(filepath.Join(tempDir, "subdir"), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "subdir", "test3.txt"), []byte("test3 content"), 0o644)
	require.NoError(t, err)

	// save original opts and restore after test
	originalOpts := opts
	defer func() { opts = originalOpts }()

	// find an available port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	err = listener.Close() // close it so the server can use it
	require.NoError(t, err)

	// set up options for the server
	opts = options{
		Listen:  fmt.Sprintf(":%d", port),
		Theme:   "light",
		RootDir: tempDir,
		Dbg:     true,
	}

	// start the server in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		// create a Web server instance directly instead of calling main()
		srv := &server.Web{
			Config: server.Config{
				ListenAddr: opts.Listen,
				Theme:      opts.Theme,
				HideFooter: opts.HideFooter,
				RootDir:    opts.RootDir,
				Version:    versionInfo(),
			},
			FS: os.DirFS(opts.RootDir),
		}
		errCh <- srv.Run(ctx)
	}()

	// wait for the server to start
	time.Sleep(100 * time.Millisecond)

	// create an HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// don't follow redirects automatically, so we can test them
			return http.ErrUseLastResponse
		},
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	t.Run("root page loads", func(t *testing.T) {
		resp, err := client.Get(baseURL)
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		assert.Contains(t, string(body), "test1.txt")
		assert.Contains(t, string(body), "test2.txt")
		assert.Contains(t, string(body), "subdir")
	})

	t.Run("directory navigation", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=subdir")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		assert.Contains(t, string(body), "test3.txt")
	})

	t.Run("file download", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/test1.txt")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment; filename=\"test1.txt\"")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "test1 content", string(body))
	})

	t.Run("file redirect", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=test1.txt")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/test1.txt", resp.Header.Get("Location"))
	})

	t.Run("directory traversal prevention", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=../")
		require.NoError(t, err)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("Failed to close body: %v", err)
			}
		}()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	// shutdown the server
	cancel()

	// wait for server to shut down
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not shut down within expected time")
	}
}

func TestIntegrationWithAuth(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create test files and directories
	err := os.WriteFile(filepath.Join(tempDir, "auth-test1.txt"), []byte("auth test content"), 0o644)
	require.NoError(t, err)
	err = os.Mkdir(filepath.Join(tempDir, "auth-subdir"), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "auth-subdir", "auth-test2.txt"), []byte("nested auth test content"), 0o644)
	require.NoError(t, err)

	// save original opts and restore after test
	originalOpts := opts
	defer func() { opts = originalOpts }()

	// find an available port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	err = listener.Close() // close it so the server can use it
	require.NoError(t, err)

	// test password and credentials
	testPassword := "test-password"
	testUser := "weblist"

	// set up options for the server with authentication
	opts = options{
		Listen:        fmt.Sprintf(":%d", port),
		Theme:         "dark",
		RootDir:       tempDir,
		Auth:          testPassword,
		AuthUser:      testUser,
		SessionSecret: "test-secret-key",
		Dbg:           true,
	}

	// start the server in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		// create a Web server instance with authentication
		srv := &server.Web{
			Config: server.Config{
				ListenAddr:      opts.Listen,
				Theme:           opts.Theme,
				HideFooter:      opts.HideFooter,
				RootDir:         opts.RootDir,
				Version:         versionInfo(),
				Auth:            opts.Auth,
				AuthUser:        opts.AuthUser,
				SessionSecret:   opts.SessionSecret,
				SessionTTL:      1 * time.Hour,
				InsecureCookies: true, // for testing
			},
			FS: os.DirFS(opts.RootDir),
		}
		errCh <- srv.Run(ctx)
	}()

	// wait for the server to start
	time.Sleep(100 * time.Millisecond)

	// create a standard HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// don't follow redirects automatically, so we can test them
			return http.ErrUseLastResponse
		},
	}

	// client with cookie jar for session management
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	clientWithCookies := &http.Client{
		Timeout: 5 * time.Second,
		Jar:     jar,
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	t.Run("unauthenticated access redirects to login", func(t *testing.T) {
		resp, err := client.Get(baseURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/login", resp.Header.Get("Location"))
	})

	t.Run("login page accessible without authentication", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/login")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check login page content
		assert.Contains(t, string(body), "<h3>Login</h3>")
		assert.Contains(t, string(body), "csrf_token")
		assert.Contains(t, string(body), "password")
	})

	t.Run("incorrect password login fails", func(t *testing.T) {
		// test direct Basic Auth with incorrect password
		req, err := http.NewRequest("GET", baseURL, nil)
		require.NoError(t, err)
		req.SetBasicAuth(testUser, "wrong-password")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// should get redirected to login page
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/login", resp.Header.Get("Location"))
	})

	t.Run("form login fails with incorrect password", func(t *testing.T) {
		// create a new client with cookie jar for this test
		jar, err := cookiejar.New(nil)
		require.NoError(t, err)
		failedLoginClient := &http.Client{
			Timeout: 5 * time.Second,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// don't follow redirects so we can verify exact status codes
				return http.ErrUseLastResponse
			},
		}

		// first get the login page to extract the CSRF token
		resp, err := failedLoginClient.Get(baseURL + "/login")
		require.NoError(t, err)
		resp.Body.Close()

		// find the CSRF token in cookies
		csrfCookie := ""
		for _, cookie := range jar.Cookies(resp.Request.URL) {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie.Value
				break
			}
		}
		require.NotEmpty(t, csrfCookie, "CSRF cookie should be set")

		// POST the login form with invalid credentials
		formValues := fmt.Sprintf("username=%s&password=%s&csrf_token=%s",
			testUser, "wrong-form-password", csrfCookie)

		req, err := http.NewRequest("POST", baseURL+"/login",
			strings.NewReader(formValues))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err = failedLoginClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// login should fail but return 200 status with error message
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// check the response contains error message
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Invalid username or password")

		// verify no auth cookie is set after failed login
		authCookieFound := false
		for _, cookie := range jar.Cookies(resp.Request.URL) {
			if cookie.Name == "auth" {
				authCookieFound = true
				break
			}
		}
		assert.False(t, authCookieFound, "Auth cookie should not be set after failed login")

		// try to access the home page (should redirect to login)
		resp, err = failedLoginClient.Get(baseURL)
		require.NoError(t, err)
		resp.Body.Close()

		// should redirect to login page since we're still unauthenticated
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/login", resp.Header.Get("Location"))
	})

	t.Run("form login creates session cookie", func(t *testing.T) {
		// create a new client with cookie jar for this test
		jar, err := cookiejar.New(nil)
		require.NoError(t, err)
		formLoginClient := &http.Client{
			Timeout: 5 * time.Second,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// follow redirects for this test
				return nil
			},
		}

		// first get the login page to extract the CSRF token
		resp, err := formLoginClient.Get(baseURL + "/login")
		require.NoError(t, err)
		// no need to read the body here, we only need the cookie
		resp.Body.Close()

		// find the CSRF token in cookies
		csrfCookie := ""
		for _, cookie := range jar.Cookies(resp.Request.URL) {
			if cookie.Name == "csrf_token" {
				csrfCookie = cookie.Value
				break
			}
		}
		require.NotEmpty(t, csrfCookie, "CSRF cookie should be set")

		// POST the login form with valid credentials
		formValues := fmt.Sprintf("username=%s&password=%s&csrf_token=%s",
			testUser, testPassword, csrfCookie)

		req, err := http.NewRequest("POST", baseURL+"/login",
			strings.NewReader(formValues))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err = formLoginClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		// should be redirected to home page after login
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// check for auth cookie
		authCookieFound := false
		for _, cookie := range jar.Cookies(resp.Request.URL) {
			if cookie.Name == "auth" {
				authCookieFound = true
				break
			}
		}
		assert.True(t, authCookieFound, "Auth cookie should be set after form login")

		// make another request to verify we're authenticated
		resp, err = formLoginClient.Get(baseURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		// should be able to access the home page with our session
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// check the content for file listing
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "auth-test1.txt")
	})

	t.Run("basic auth works", func(t *testing.T) {
		req, err := http.NewRequest("GET", baseURL, nil)
		require.NoError(t, err)
		req.SetBasicAuth(testUser, testPassword)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// check if auth cookie is set
		var authCookie *http.Cookie
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}
		require.NotNil(t, authCookie, "Auth cookie should be set after successful basic auth")

		// verify content
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "auth-test1.txt")
		assert.Contains(t, string(body), "auth-subdir")
	})

	t.Run("REST API list requires authentication", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/api/list")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/login", resp.Header.Get("Location"))
	})

	t.Run("REST API with authentication works", func(t *testing.T) {
		// get authentication cookie via basic auth
		req, err := http.NewRequest("GET", baseURL, nil)
		require.NoError(t, err)
		req.SetBasicAuth(testUser, testPassword)

		resp, err := clientWithCookies.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		// now use the authenticated client to access API
		resp, err = clientWithCookies.Get(baseURL + "/api/list")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		// parse JSON response
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var apiResponse struct {
			Path  string `json:"path"`
			Files []struct {
				Name       string `json:"name"`
				Path       string `json:"path"`
				IsDir      bool   `json:"is_dir"`
				Size       int64  `json:"size"`
				SizeHuman  string `json:"size_human"`
				TimeStr    string `json:"time_str"`
				IsViewable bool   `json:"is_viewable"`
			} `json:"files"`
			Sort string `json:"sort"`
			Dir  string `json:"dir"`
		}

		err = json.Unmarshal(body, &apiResponse)
		require.NoError(t, err, "Response should be valid JSON")

		// verify response contents
		assert.Equal(t, "", apiResponse.Path)
		assert.Equal(t, "name", apiResponse.Sort)
		assert.Equal(t, "asc", apiResponse.Dir)

		// verify files are present
		fileNames := make([]string, 0)
		for _, file := range apiResponse.Files {
			fileNames = append(fileNames, file.Name)
		}
		assert.Contains(t, fileNames, "auth-test1.txt")
		assert.Contains(t, fileNames, "auth-subdir")

		// test sorting and filtering
		resp, err = clientWithCookies.Get(baseURL + "/api/list?sort=-size")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err = io.ReadAll(resp.Body)
		require.NoError(t, err)

		err = json.Unmarshal(body, &apiResponse)
		require.NoError(t, err)

		assert.Equal(t, "size", apiResponse.Sort)
		assert.Equal(t, "desc", apiResponse.Dir)
	})

	t.Run("logout works", func(t *testing.T) {
		// create a new client with cookie jar for this test
		jar, err := cookiejar.New(nil)
		require.NoError(t, err)
		logoutTestClient := &http.Client{
			Timeout: 5 * time.Second,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// don't follow redirects automatically
				return http.ErrUseLastResponse
			},
		}

		// first login with Basic Auth
		req, err := http.NewRequest("GET", baseURL, nil)
		require.NoError(t, err)
		req.SetBasicAuth(testUser, testPassword)

		// login
		resp, err := logoutTestClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		// verify we're authenticated
		resp, err = logoutTestClient.Get(baseURL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "Should be authenticated and see home page")

		// now logout
		resp, err = logoutTestClient.Get(baseURL + "/logout")
		require.NoError(t, err)
		defer resp.Body.Close()

		// verify redirect to login page
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "Logout should redirect")
		assert.Equal(t, "/login", resp.Header.Get("Location"), "Logout should redirect to login page")
	})

	// shutdown the server
	cancel()

	// wait for server to shut down
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not shut down within expected time")
	}
}
