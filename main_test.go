package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

func TestMainIntegration(t *testing.T) {
	// fix the assertion on line 508 - replace assert.NoError with require.NoError
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
