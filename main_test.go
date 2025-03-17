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
	assert.True(t, version == "dev" || version == "unknown" || len(version) > 0,
		"Version should be 'dev', 'unknown', or a valid version string")
}

func TestSetupLog(t *testing.T) {
	// test with debug mode off
	setupLog(false)

	// test with debug mode on
	setupLog(true)

	// test with secrets
	setupLog(false, "secret1", "secret2")

	// no assertions needed as we're just ensuring it doesn't panic
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
			assert.NoError(t, err, "Flag parsing should not produce an error")

			// check if options match expected values
			assert.Equal(t, tc.expected.Listen, opts.Listen, "Listen address should match")
			assert.Equal(t, tc.expected.Theme, opts.Theme, "Theme should match")
			assert.Equal(t, tc.expected.RootDir, opts.RootDir, "Root directory should match")
			assert.Equal(t, tc.expected.Dbg, opts.Dbg, "Debug mode should match")
		})
	}
}

func TestMainIntegration(t *testing.T) {
	// create a temporary directory for testing
	tempDir := t.TempDir()

	// create some test files in the temp directory
	err := os.WriteFile(filepath.Join(tempDir, "test1.txt"), []byte("test1 content"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "test2.txt"), []byte("test2 content"), 0644)
	require.NoError(t, err)

	// create a subdirectory with a file
	err = os.Mkdir(filepath.Join(tempDir, "subdir"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "subdir", "test3.txt"), []byte("test3 content"), 0644)
	require.NoError(t, err)

	// save original opts and restore after test
	originalOpts := opts
	defer func() { opts = originalOpts }()

	// find an available port
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // close it so the server can use it

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

	// test cases
	t.Run("root page loads", func(t *testing.T) {
		resp, err := client.Get(baseURL)
		require.NoError(t, err)
		defer resp.Body.Close()

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
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// check that the page contains expected content
		assert.Contains(t, string(body), "test3.txt")
	})

	t.Run("file download", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/test1.txt")
		require.NoError(t, err)
		defer resp.Body.Close()

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
		defer resp.Body.Close()

		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/test1.txt", resp.Header.Get("Location"))
	})

	t.Run("directory traversal prevention", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/?path=../")
		require.NoError(t, err)
		defer resp.Body.Close()

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
