package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestShouldExcludeForSFTP(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		exclude  []string
		expected bool
	}{
		{
			name:     "no exclusions",
			path:     "path/to/file.txt",
			exclude:  []string{},
			expected: false,
		},
		{
			name:     "exact match",
			path:     "file.txt",
			exclude:  []string{"file.txt"},
			expected: true,
		},
		{
			name:     "directory component match",
			path:     "path/to/.git/config",
			exclude:  []string{".git"},
			expected: true,
		},
		{
			name:     "end with pattern",
			path:     "path/to/.git",
			exclude:  []string{".git"},
			expected: true,
		},
		{
			name:     "no match",
			path:     "path/to/file.txt",
			exclude:  []string{".git", "node_modules"},
			expected: false,
		},
		{
			name:     "multiple exclusions, one match",
			path:     "path/to/node_modules/file.txt",
			exclude:  []string{".git", "node_modules"},
			expected: true,
		},
	}

	// create a jailed filesystem for testing
	jailed := &jailedFilesystem{
		rootDir:  "/tmp",
		excludes: []string{},
		fsys:     os.DirFS("/tmp"),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jailed.excludes = tc.exclude
			result := jailed.shouldExclude(tc.path)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSecurePath(t *testing.T) {
	// create a temporary directory for testing
	rootDir, err := os.MkdirTemp("", "sftp-secpath-test")
	require.NoError(t, err)
	defer os.RemoveAll(rootDir)

	// create a subdirectory
	subDir := filepath.Join(rootDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// create a test file
	testFile := filepath.Join(rootDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	// create test file in subdirectory
	subTestFile := filepath.Join(subDir, "subtest.txt")
	err = os.WriteFile(subTestFile, []byte("subdir test content"), 0644)
	require.NoError(t, err)

	// create a jailed filesystem for testing
	jailed := &jailedFilesystem{
		rootDir:  rootDir,
		excludes: []string{".git", "private"},
		fsys:     os.DirFS(rootDir),
	}

	// create a private directory that should be excluded
	privateDir := filepath.Join(rootDir, "private")
	err = os.Mkdir(privateDir, 0755)
	require.NoError(t, err)
	privateFile := filepath.Join(privateDir, "secret.txt")
	err = os.WriteFile(privateFile, []byte("private content"), 0644)
	require.NoError(t, err)

	// test cases
	cases := []struct {
		name        string
		requestPath string
		shouldPass  bool
		expected    string
	}{
		{
			name:        "root path",
			requestPath: "/",
			shouldPass:  true,
			expected:    ".",
		},
		{
			name:        "empty path",
			requestPath: "",
			shouldPass:  true, // with simplified checks, empty path is now allowed and treated as root
			expected:    ".",
		},
		{
			name:        "direct file",
			requestPath: "/test.txt",
			shouldPass:  true,
			expected:    "test.txt",
		},
		{
			name:        "subdirectory",
			requestPath: "/subdir",
			shouldPass:  true,
			expected:    "subdir",
		},
		{
			name:        "file in subdirectory",
			requestPath: "/subdir/subtest.txt",
			shouldPass:  true,
			expected:    "subdir/subtest.txt",
		},
		{
			name:        "traversal with double dot",
			requestPath: "/subdir/../test.txt",
			shouldPass:  false, // we still check for path traversal for defense in depth
		},
		{
			name:        "traversal with double dot at start",
			requestPath: "/../etc/passwd",
			shouldPass:  false,
		},
		{
			name:        "absolute path outside root",
			requestPath: "/etc/passwd",
			shouldPass:  true, // actual access restrictions are handled by fs.FS
			expected:    "etc/passwd",
		},
		{
			name:        "excluded directory",
			requestPath: "/private/secret.txt",
			shouldPass:  false,
		},
		{
			name:        "path with . component",
			requestPath: "/./test.txt",
			shouldPass:  true,
			expected:    "test.txt",
		},
		{
			name:        "complex traversal attempt",
			requestPath: "/subdir/../private/../test.txt",
			shouldPass:  false, // we still check for path traversal with ..
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := jailed.securePath(tc.requestPath)
			if tc.shouldPass {
				require.NoError(t, err, "Expected path to be allowed")
				assert.Equal(t, tc.expected, result)
			} else {
				require.Error(t, err, "Expected path to be rejected")
				t.Logf("Got expected error: %v", err)
			}
		})
	}
}

func TestSFTPServerIntegration(t *testing.T) {
	// skip this test in short mode as it's an integration test
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// create a test directory structure
	rootDir := setupTestDirectoryStructure(t)
	defer os.RemoveAll(rootDir)

	// start SFTP server
	port, cleanup := startSFTPServer(t, rootDir)
	defer cleanup()

	// create SFTP client
	client := createSFTPClient(t, port)
	defer client.Close()

	// verify correct access to the root directory
	testRootDirectoryAccess(t, client)

	// verify subdirectory access
	testSubdirectoryAccess(t, client)

	// verify exclusion pattern works
	testExclusionPattern(t, client)

	// verify path traversal prevention
	testPathTraversal(t, client)

	// verify read-only mode
	testReadOnlyMode(t, client)
}

// setupTestDirectoryStructure creates a test directory structure and returns the root path
func setupTestDirectoryStructure(t *testing.T) string {
	t.Helper()

	// create a temporary root directory
	rootDir, err := os.MkdirTemp("", "sftp-test")
	require.NoError(t, err)

	// create a file in the root
	rootFile := filepath.Join(rootDir, "root-file.txt")
	err = os.WriteFile(rootFile, []byte("This is the root file"), 0644)
	require.NoError(t, err)

	// create a file that should be excluded
	excludedFile := filepath.Join(rootDir, ".git")
	err = os.Mkdir(excludedFile, 0755)
	require.NoError(t, err)
	gitConfig := filepath.Join(excludedFile, "config")
	err = os.WriteFile(gitConfig, []byte("This should be excluded"), 0644)
	require.NoError(t, err)

	// create a subdirectory
	subDir := filepath.Join(rootDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// create a file in the subdirectory
	subFile := filepath.Join(subDir, "sub-file.txt")
	err = os.WriteFile(subFile, []byte("This is a file in the subdirectory"), 0644)
	require.NoError(t, err)

	// create a nested subdirectory
	nestedDir := filepath.Join(subDir, "nested")
	err = os.Mkdir(nestedDir, 0755)
	require.NoError(t, err)

	// create a file in the nested subdirectory
	nestedFile := filepath.Join(nestedDir, "nested-file.txt")
	err = os.WriteFile(nestedFile, []byte("This is a file in the nested subdirectory"), 0644)
	require.NoError(t, err)

	// log the test directory structure
	t.Logf("Created test directory structure in %s", rootDir)
	t.Logf("- root-file.txt")
	t.Logf("- .git/ (excluded directory)")
	t.Logf("  - config")
	t.Logf("- subdir/")
	t.Logf("  - sub-file.txt")
	t.Logf("  - nested/")
	t.Logf("    - nested-file.txt")

	return rootDir
}

// startSFTPServer starts an SFTP server and returns the port and a cleanup function
func startSFTPServer(t *testing.T, rootDir string) (port string, cleanup func()) {
	t.Helper()

	// find an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port = fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	t.Logf("SFTP test: allocated port %s", port)
	require.NoError(t, listener.Close())

	// create a temporary key file for the server
	keyFile, err := os.CreateTemp("", "sftp-test-key-*")
	require.NoError(t, err)
	keyPath := keyFile.Name()
	keyFile.Close()
	defer os.Remove(keyPath)

	// create and start the SFTP server
	sftpServer := &SFTP{
		Config: Config{
			SFTPUser:    "testuser",
			SFTPAddress: "127.0.0.1:" + port,
			Auth:        "testpass",
			RootDir:     rootDir,
			Exclude:     []string{".git"},
			SFTPKeyFile: keyPath,
		},
		FS: os.DirFS(rootDir),
	}

	ctx, cancel := context.WithCancel(context.Background())

	// use a channel to detect when server is ready
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	// start server in a goroutine
	go func() {
		t.Logf("SFTP test: starting server on 127.0.0.1:%s", port)

		// create another goroutine that checks if the server is listening
		go func() {
			// wait a bit to allow the server to initialize
			time.Sleep(100 * time.Millisecond)

			// keep checking if the port is open until it is or we timeout
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 100*time.Millisecond)
				if err == nil {
					t.Logf("SFTP test: server is listening on port %s", port)
					conn.Close()
					close(readyCh)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}

			t.Logf("SFTP test: server failed to start listening on port %s within timeout", port)
		}()

		err := sftpServer.Run(ctx)
		if err != nil && ctx.Err() == nil {
			t.Logf("SFTP server error: %v", err)
			errCh <- err
		}
	}()

	// wait for either the server to be ready or an error to occur
	select {
	case <-readyCh:
		t.Logf("SFTP test: server ready")
	case err := <-errCh:
		t.Fatalf("SFTP server failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("SFTP server failed to start within timeout")
	}

	// give the server additional time to fully initialize the SSH handler
	time.Sleep(200 * time.Millisecond)

	// return port and cleanup function
	return port, func() {
		t.Logf("SFTP test: shutting down server")
		cancel()
		time.Sleep(200 * time.Millisecond) // give server time to shut down
	}
}

// createSFTPClient creates and returns an SFTP client connected to the test server
func createSFTPClient(t *testing.T, port string) *sftp.Client {
	t.Helper()

	// configure SSH client
	sshConfig := &ssh.ClientConfig{
		User: "testuser",
		Auth: []ssh.AuthMethod{
			ssh.Password("testpass"),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// try connecting with retries
	var sshClient *ssh.Client
	var err error

	address := "127.0.0.1:" + port
	t.Logf("SFTP test: attempting to connect to %s", address)

	// retry logic for establishing SSH connection
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sshClient, err = ssh.Dial("tcp", address, sshConfig)
		if err == nil {
			break
		}
		t.Logf("SFTP test: connection attempt failed: %v, retrying...", err)
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err, "Failed to connect to SSH server after multiple attempts")
	t.Logf("SFTP test: SSH connection established to %s", address)

	// create SFTP client
	client, err := sftp.NewClient(sshClient)
	require.NoError(t, err, "Failed to create SFTP client")
	t.Logf("SFTP test: SFTP client created successfully")

	return client
}

// testRootDirectoryAccess verifies access to files in the root directory
func testRootDirectoryAccess(t *testing.T, client *sftp.Client) {
	t.Helper()

	// list files in root directory
	entries, err := client.ReadDir("/")
	require.NoError(t, err, "Should be able to list root directory")

	// create a map of filenames for easier checking
	fileMap := make(map[string]bool)
	for _, entry := range entries {
		fileMap[entry.Name()] = true
		t.Logf("Root directory entry: %s", entry.Name())
	}

	// verify root file is present
	assert.True(t, fileMap["root-file.txt"], "Root file should be listed")
	assert.True(t, fileMap["subdir"], "Subdirectory should be listed")
	// note: The .. entry might not be visible in all SFTP client implementations,
	// even though we add it in the server side

	// verify excluded file is not present
	assert.False(t, fileMap[".git"], "Excluded directory should not be listed")

	// read the root file
	file, err := client.Open("/root-file.txt")
	require.NoError(t, err, "Should be able to open root file")
	defer file.Close()

	content, err := io.ReadAll(file)
	require.NoError(t, err, "Should be able to read root file")
	assert.Equal(t, "This is the root file", string(content), "Root file content should match")
}

// testSubdirectoryAccess verifies access to files in subdirectories
func testSubdirectoryAccess(t *testing.T, client *sftp.Client) {
	t.Helper()

	// list files in subdirectory
	entries, err := client.ReadDir("/subdir")
	require.NoError(t, err, "Should be able to list subdirectory")

	// create a map of filenames for easier checking
	fileMap := make(map[string]bool)
	for _, entry := range entries {
		fileMap[entry.Name()] = true
		t.Logf("Subdir entry: %s", entry.Name())
	}

	// verify subdirectory contents
	assert.True(t, fileMap["sub-file.txt"], "Subdirectory file should be listed")
	assert.True(t, fileMap["nested"], "Nested directory should be listed")
	// note: The .. entry might not be visible in all SFTP client implementations,
	// even though we add it in the server side

	// read the subdirectory file
	file, err := client.Open("/subdir/sub-file.txt")
	require.NoError(t, err, "Should be able to open subdirectory file")
	defer file.Close()

	content, err := io.ReadAll(file)
	require.NoError(t, err, "Should be able to read subdirectory file")
	assert.Equal(t, "This is a file in the subdirectory", string(content), "Subdirectory file content should match")

	// test navigation through nested directories
	entries, err = client.ReadDir("/subdir/nested")
	require.NoError(t, err, "Should be able to list nested directory")

	fileMap = make(map[string]bool)
	for _, entry := range entries {
		fileMap[entry.Name()] = true
		t.Logf("Nested directory entry: %s", entry.Name())
	}

	assert.True(t, fileMap["nested-file.txt"], "Nested directory file should be listed")
	// note: The .. entry might not be visible in all SFTP client implementations,
	// even though we add it in the server side

	// test navigating up from subdirectory
	entries, err = client.ReadDir("/subdir/..")
	require.NoError(t, err, "Should be able to list parent directory via ..")

	// this should show the root directory contents
	fileMap = make(map[string]bool)
	for _, entry := range entries {
		fileMap[entry.Name()] = true
	}

	assert.True(t, fileMap["root-file.txt"], "Navigating up should show root file")
	assert.True(t, fileMap["subdir"], "Navigating up should show subdirectory")
}

// testExclusionPattern verifies that excluded files are not accessible
func testExclusionPattern(t *testing.T, client *sftp.Client) {
	t.Helper()

	// try to list the excluded directory - should fail
	_, err := client.ReadDir("/.git")
	assert.Error(t, err, "Should not be able to list excluded directory")
	t.Logf("Got expected error trying to list .git: %v", err)

	// try to access a file in the excluded directory - should fail
	_, err = client.Open("/.git/config")
	assert.Error(t, err, "Should not be able to open file in excluded directory")
	t.Logf("Got expected error trying to access .git/config: %v", err)
}

// testPathTraversal verifies that path traversal is prevented
func testPathTraversal(t *testing.T, client *sftp.Client) {
	t.Helper()

	// try various path traversal techniques
	traversalPaths := []string{
		"/../etc/passwd",                        // simple traversal with absolute path
		"/subdir/../../etc/passwd",              // traversal from subdirectory
		"/subdir/nested/../../../../etc/passwd", // deep traversal
	}

	for _, path := range traversalPaths {
		// try to open file with path traversal - should fail
		// with our simplified approach, it will either:
		// 1. Fail with "path traversal attempt detected" if it contains ".."
		// 2. Fail with "file does not exist" if fs.FS blocks access
		_, err := client.Open(path)
		assert.Error(t, err, "Should not be able to traverse outside root: %s", path)
		t.Logf("Got expected error for path traversal %s: %v", path, err)
	}

	// special case for ".." in the root - this should just show root again
	// this is normal behavior for SFTP clients
	entries, err := client.ReadDir("/..")
	assert.NoError(t, err, "Should be able to list /..")

	// make sure we're still seeing the root directory
	found := false
	for _, entry := range entries {
		if entry.Name() == "root-file.txt" {
			found = true
			break
		}
	}
	assert.True(t, found, "Should find root-file.txt in /..")
}

// testReadOnlyMode verifies that write operations are not allowed
func testReadOnlyMode(t *testing.T, client *sftp.Client) {
	t.Helper()

	// try to create a new file
	f, err := client.Create("/newfile.txt")
	assert.Error(t, err, "Should not be able to create a file")
	if f != nil {
		f.Close()
	}

	// try to remove a file
	err = client.Remove("/root-file.txt")
	assert.Error(t, err, "Should not be able to remove a file")

	// try to rename a file
	err = client.Rename("/root-file.txt", "/renamed.txt")
	assert.Error(t, err, "Should not be able to rename a file")

	// try to create a directory
	err = client.Mkdir("/newdir")
	assert.Error(t, err, "Should not be able to create a directory")

	// try to remove a directory
	err = client.RemoveDirectory("/subdir")
	assert.Error(t, err, "Should not be able to remove a directory")
}

// TestSFTPKeyPersistence tests that the SSH key is properly saved and reused
func TestSFTPKeyPersistence(t *testing.T) {
	// skip this test in short mode as it's an integration test
	if testing.Short() {
		t.Skip("skipping key persistence test in short mode")
	}

	// create a temporary key file
	keyFile, err := os.CreateTemp("", "sftp-test-key-*")
	require.NoError(t, err)
	keyPath := keyFile.Name()
	keyFile.Close()
	defer os.Remove(keyPath)

	// create a test directory
	rootDir, err := os.MkdirTemp("", "sftp-key-test")
	require.NoError(t, err)
	defer os.RemoveAll(rootDir)

	// create a test file
	testFilePath := filepath.Join(rootDir, "test.txt")
	err = os.WriteFile(testFilePath, []byte("test content"), 0644)
	require.NoError(t, err)

	// start first server with the key file
	config := Config{
		SFTPUser:    "testuser",
		SFTPAddress: "127.0.0.1:0", // use any available port
		Auth:        "testpass",
		RootDir:     rootDir,
		SFTPKeyFile: keyPath,
	}

	// create a listener to get an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	t.Logf("SFTP key test: allocated port %s", port)
	require.NoError(t, listener.Close())

	// update the config with the allocated port
	config.SFTPAddress = "127.0.0.1:" + port

	// create and start the first server
	sftpServer1 := &SFTP{
		Config: config,
		FS:     os.DirFS(rootDir),
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	// channel to detect when server is ready
	ready1 := make(chan struct{})
	go func() {
		// check server is listening
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < 20; i++ {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				close(ready1)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("First server failed to start")
	}()

	// start the first server
	go func() {
		err := sftpServer1.Run(ctx1)
		if err != nil && ctx1.Err() == nil {
			t.Errorf("First server error: %v", err)
		}
	}()

	// wait for first server to be ready
	select {
	case <-ready1:
		t.Log("First server ready")
	case <-time.After(3 * time.Second):
		t.Fatal("First server failed to start within timeout")
	}

	// get the key file info before stopping the first server
	stat1, err := os.Stat(keyPath)
	require.NoError(t, err)
	t.Logf("Key file created with size: %d bytes", stat1.Size())

	// stop the first server
	cancel1()
	time.Sleep(500 * time.Millisecond)

	// start second server with the same key file
	sftpServer2 := &SFTP{
		Config: config,
		FS:     os.DirFS(rootDir),
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	// channel to detect when server is ready
	ready2 := make(chan struct{})
	go func() {
		// check server is listening
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < 20; i++ {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				close(ready2)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("Second server failed to start")
	}()

	// start the second server
	go func() {
		err := sftpServer2.Run(ctx2)
		if err != nil && ctx2.Err() == nil {
			t.Errorf("Second server error: %v", err)
		}
	}()

	// wait for second server to be ready
	select {
	case <-ready2:
		t.Log("Second server ready")
	case <-time.After(3 * time.Second):
		t.Fatal("Second server failed to start within timeout")
	}

	// get the key file info after starting the second server
	stat2, err := os.Stat(keyPath)
	require.NoError(t, err)
	t.Logf("Key file after second server start size: %d bytes", stat2.Size())

	// the file size and modification time should be the same if the key was reused
	assert.Equal(t, stat1.Size(), stat2.Size(), "Key file size should be the same if key was reused")
	assert.Equal(t, stat1.ModTime().Unix(), stat2.ModTime().Unix(), "Key file modification time should be the same if key was reused")

	// stop the second server
	cancel2()
}
