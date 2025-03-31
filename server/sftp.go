package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP represents the SFTP server.
type SFTP struct {
	Config
	FS fs.FS

	// simple rate limiter for authentication attempts
	ipAttempts   map[string]ipAttemptsInfo
	ipAttemptsMu sync.Mutex
}

// ipAttemptsInfo tracks authentication attempts from an IP
type ipAttemptsInfo struct {
	count     int       // number of attempts
	firstSeen time.Time // when the first attempt was seen
	lastSeen  time.Time // when the most recent attempt was seen
}

// Run starts the SFTP server.
func (s *SFTP) Run(ctx context.Context) error {
	// validate required fields
	if err := s.validateConfig(); err != nil {
		return err
	}

	// configure SSH server
	config, err := s.setupSSHServerConfig()
	if err != nil {
		return err
	}

	// start listener
	listener, err := net.Listen("tcp", s.SFTPAddress)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.SFTPAddress, err)
	}
	defer listener.Close()

	log.Printf("[INFO] starting SFTP server on %s with username: %s", s.SFTPAddress, s.SFTPUser)

	// channel for connection errors
	errorCh := make(chan error, 1)

	// handle connections in a separate goroutine
	go func() {
		for {
			// check for shutdown signal
			select {
			case <-ctx.Done():
				return
			default:
			}

			// accept new connection
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					// context was canceled, normal shutdown
					return
				}
				errorCh <- fmt.Errorf("accept error: %w", err)
				return
			}

			// handle connection in a goroutine
			go s.handleConnection(conn, config)
		}
	}()

	// wait for shutdown signal or error
	select {
	case err := <-errorCh:
		return fmt.Errorf("SFTP server failed: %w", err)
	case <-ctx.Done():
		log.Printf("[DEBUG] SFTP server shutdown initiated")
		if err := listener.Close(); err != nil {
			log.Printf("[WARN] error closing listener: %v", err)
		}
		log.Printf("[INFO] SFTP server shutdown completed")
		return nil
	}
}

// handleConnection handles a single SSH connection
func (s *SFTP) handleConnection(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()

	// apply idle timeout to the connection
	timeoutConn := &timeoutConn{
		Conn:         conn,
		idleTimeout:  10 * time.Minute,
		lastActivity: time.Now(),
	}

	// perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(timeoutConn, config)
	if err != nil {
		log.Printf("[WARN] SSH handshake failed: %v", err)
		return
	}
	defer sshConn.Close()

	log.Printf("[DEBUG] new SSH connection from %s (%s)", sshConn.RemoteAddr(), sshConn.ClientVersion())

	// discard global requests - we don't use them
	go ssh.DiscardRequests(reqs)

	// handle channel requests (sessions)
	for newChan := range chans {
		// accept only session channels
		if newChan.ChannelType() != "session" {
			if err := newChan.Reject(ssh.UnknownChannelType, "unknown channel type"); err != nil {
				log.Printf("[WARN] error rejecting channel: %v", err)
			}
			continue
		}

		// accept the channel
		channel, requests, err := newChan.Accept()
		if err != nil {
			log.Printf("[WARN] could not accept channel: %v", err)
			continue
		}

		// handle session requests in a goroutine
		go s.handleSession(channel, requests)
	}
}

// handleSession processes a single SSH session
func (s *SFTP) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	for req := range requests {
		log.Printf("[DEBUG] SSH Request: %s, WantReply: %v", req.Type, req.WantReply)

		switch req.Type {
		case "subsystem":
			// only handle SFTP subsystem
			if len(req.Payload) < 5 {
				replyRequest(req, false, "invalid subsystem request")
				continue
			}

			subsystemName := string(req.Payload[4:])
			if subsystemName != "sftp" {
				replyRequest(req, false, "unsupported subsystem")
				continue
			}

			// accept the SFTP subsystem request
			replyRequest(req, true, "")

			// start SFTP server
			s.startSFTPServer(channel)
			return

		case "shell":
			// accept shell request but only send a message and close
			replyRequest(req, true, "")
			if _, err := io.WriteString(channel, "SFTP access only, interactive shell not available\r\n"); err != nil {
				log.Printf("[WARN] error writing to channel: %v", err)
			}
			return

		case "pty-req", "env":
			// accept these for compatibility with clients
			replyRequest(req, true, "")

		default:
			// reject other requests
			replyRequest(req, false, "unsupported request type")
		}
	}
}

// replyRequest replies to an SSH request with appropriate logging
func replyRequest(req *ssh.Request, accept bool, logMsg string) {
	if err := req.Reply(accept, nil); err != nil {
		log.Printf("[WARN] Failed to reply to %s request: %v", req.Type, err)
		return
	}

	if logMsg != "" {
		if accept {
			log.Printf("[DEBUG] Accepted %s request: %s", req.Type, logMsg)
		} else {
			log.Printf("[WARN] Rejected %s request: %s", req.Type, logMsg)
		}
	}
}

// startSFTPServer starts the SFTP server on the given channel
func (s *SFTP) startSFTPServer(channel ssh.Channel) {
	// create a jailed filesystem that restricts access to the root directory
	jailed := &jailedFilesystem{
		rootDir:  s.RootDir,
		excludes: s.Exclude,
		fsys:     s.FS,
	}

	// create handlers for our custom jailed filesystem
	handlers := sftp.Handlers{
		FileGet:  jailed, // handle file reads
		FilePut:  jailed, // handle file writes (will be denied)
		FileCmd:  jailed, // handle file operations
		FileList: jailed, // handle directory listings
	}

	// create a RequestServer with our custom handlers
	server := sftp.NewRequestServer(
		channel,
		handlers,
		// using ReadOnly option to ensure we don't allow writes
	)

	defer server.Close()

	log.Printf("[INFO] Starting SFTP subsystem with root directory: %s", s.RootDir)

	// start the SFTP server - this will block until the channel is closed
	if err := server.Serve(); err != nil {
		if err == io.EOF {
			log.Printf("[DEBUG] SFTP session ended normally")
		} else {
			log.Printf("[ERROR] SFTP server terminated with error: %v", err)
		}
	}
}

// jailedFilesystem implements the sftp.Handlers interfaces to create a secure,
// read-only view of the filesystem for SFTP clients. It provides several security layers:
// 1. Path containment - prevents clients from accessing files outside the root directory
// 2. Read-only enforcement - actively denies all write/modify operations
// 3. Path filtering - excludes sensitive files/directories based on patterns
// 4. No symlink support - prevents potential security bypasses via symbolic links
//
// This is the core security boundary for the SFTP server, ensuring that remote
// users cannot access unauthorized files or modify content.
type jailedFilesystem struct {
	rootDir  string   // physical root directory path
	excludes []string // patterns to exclude
	fsys     fs.FS    // filesystem interface
}

// Fileread implements sftp.FileCmder.Fileread
func (j *jailedFilesystem) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	// add debug logging of the request path
	log.Printf("[DEBUG] SFTP: File read request for path: %s (request path)", r.Filepath)

	// validate and secure the path
	secPath, err := j.securePath(r.Filepath)
	if err != nil {
		log.Printf("[WARN] SFTP: Denied file read access to %s: %v", r.Filepath, err)
		return nil, fmt.Errorf("access denied: %w", err)
	}

	log.Printf("[DEBUG] SFTP: Secured path for file read: %s (from %s)", secPath, r.Filepath)

	// open file through fs.FS interface
	file, err := j.fsys.Open(secPath)
	if err != nil {
		log.Printf("[DEBUG] SFTP: Failed to open %s (secure path: %s): %v", r.Filepath, secPath, err)

		// improve error message for user
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", r.Filepath)
		}
		return nil, err
	}

	// check if file implements ReaderAt directly
	if ra, ok := file.(io.ReaderAt); ok {
		log.Printf("[DEBUG] SFTP: Allowed read access to %s (secure path: %s) using native ReaderAt", r.Filepath, secPath)
		return ra, nil
	}

	// get file info to check size
	info, err := file.Stat()
	if err != nil {
		if err := file.Close(); err != nil {
			log.Printf("[DEBUG] SFTP: Error closing file %s after stat error: %v", secPath, err)
		}
		log.Printf("[DEBUG] SFTP: Error getting file info for %s: %v", secPath, err)
		return nil, err
	}

	// for small files (under 10MB), just read the entire file into memory
	// this is more efficient for small files that are frequently accessed
	// while large files use a buffered approach to avoid excessive memory usage
	if info.Size() < 10*1024*1024 {
		data, err := io.ReadAll(file)
		if err := file.Close(); err != nil {
			log.Printf("[DEBUG] SFTP: Error closing file %s: %v", secPath, err)
		}

		if err != nil {
			log.Printf("[DEBUG] SFTP: Error reading file %s: %v", secPath, err)
			return nil, err
		}

		log.Printf("[DEBUG] SFTP: Allowed read access to %s (secure path: %s) using memory ReaderAt for small file", r.Filepath, secPath)
		return &memReaderAt{data: data}, nil
	}

	// for large files, use a buffered reader to avoid loading everything into memory
	log.Printf("[DEBUG] SFTP: Allowed read access to %s (secure path: %s) using buffered reader for large file", r.Filepath, secPath)
	return &bufferedFileReaderAt{
		file:     file,
		fileSize: info.Size(),
		path:     secPath,
	}, nil
}

// Filewrite implements sftp.FileCmder.Filewrite
func (j *jailedFilesystem) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	log.Printf("[WARN] SFTP: Rejected write attempt to %s (server is read-only)", r.Filepath)
	return nil, fmt.Errorf("write operations not permitted - server is in read-only mode")
}

// Filecmd implements sftp.FileCmder.Filecmd
func (j *jailedFilesystem) Filecmd(r *sftp.Request) error {
	log.Printf("[WARN] SFTP: Rejected file operation %s on %s (server is read-only)", r.Method, r.Filepath)
	return fmt.Errorf("operation not permitted - server is in read-only mode")
}

// We don't need to implement Stat or Lstat as separate methods
// The FileLister.Filelist method is used for both Stat and ReadDir operations

// Readlink implements sftp.FileLister.Readlink
func (j *jailedFilesystem) Readlink(r *sftp.Request) (string, error) {
	// we don't support symlinks for security reasons
	log.Printf("[WARN] SFTP: Rejected readlink request for %s (symlinks not supported)", r.Filepath)
	return "", fmt.Errorf("symlinks are not supported")
}

// Filelist implements sftp.FileLister interface
func (j *jailedFilesystem) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	path := r.Filepath
	log.Printf("[DEBUG] SFTP: Directory list request for path: %s (request path)", path)

	// special case for the root directory
	if path == "/" {
		infos, err := j.listRoot()
		if err != nil {
			return nil, err
		}
		return &listerat{entries: infos}, nil
	}

	// validate and secure the path
	secPath, err := j.securePath(path)
	if err != nil {
		log.Printf("[WARN] SFTP: Denied directory listing for %s: %v", path, err)
		return nil, fmt.Errorf("access denied: %w", err)
	}

	// first check if this is a file or directory
	info, err := fs.Stat(j.fsys, secPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[DEBUG] SFTP: Path does not exist: %s (secure path: %s)", path, secPath)
			return nil, fmt.Errorf("path does not exist: %s", path)
		}
		log.Printf("[DEBUG] SFTP: Failed to stat path %s (secure path: %s): %v", path, secPath, err)
		return nil, err
	}

	if !info.IsDir() {
		// this is a file, but client is trying to do an ls on it
		// openSSH SFTP server returns a directory with just this file in it
		// we'll do the same to be compatible with clients expecting this behavior
		log.Printf("[DEBUG] SFTP: Listing a single file as directory: %s", path)
		return &listerat{entries: []os.FileInfo{info}}, nil
	}

	// read directory entries
	entries, err := fs.ReadDir(j.fsys, secPath)
	if err != nil {
		log.Printf("[DEBUG] SFTP: Failed to read directory %s (secure path: %s): %v", path, secPath, err)
		return nil, err
	}

	// filter out excluded entries and convert to FileInfo
	// preallocate to approximate size (entries plus parent directory)
	fileInfos := make([]os.FileInfo, 0, len(entries)+1)
	for _, entry := range entries {
		entryPath := filepath.Join(secPath, entry.Name())

		// skip excluded files/directories
		if j.shouldExclude(entryPath) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("[DEBUG] SFTP: Failed to get info for %s: %v", entryPath, err)
			continue
		}

		fileInfos = append(fileInfos, info)
	}

	// always add parent directory entry - it's present in all directories
	// for the virtual root ("/"), it'll refer to itself
	fileInfos = append([]os.FileInfo{
		&virtualFileInfo{
			name:    "..",
			mode:    os.ModeDir | 0555, // r-xr-xr-x
			modTime: time.Now(),
			isDir:   true,
		},
	}, fileInfos...)

	log.Printf("[DEBUG] SFTP: Listed directory %s (secure path: %s) with %d entries", path, secPath, len(fileInfos))
	return &listerat{entries: fileInfos}, nil
}

// listRoot handles listing the root directory content
func (j *jailedFilesystem) listRoot() ([]os.FileInfo, error) {
	// read directory entries from the filesystem
	entries, err := fs.ReadDir(j.fsys, ".")
	if err != nil {
		log.Printf("[DEBUG] SFTP: Failed to read root directory: %v", err)
		return nil, err
	}

	// filter and convert to FileInfo
	// preallocate to approximate size (entries plus parent directory)
	fileInfos := make([]os.FileInfo, 0, len(entries)+1)

	// always add parent directory entry (in root it points to itself)
	fileInfos = append(fileInfos, &virtualFileInfo{
		name:    "..",
		mode:    os.ModeDir | 0555, // r-xr-xr-x
		modTime: time.Now(),
		isDir:   true,
	})

	for _, entry := range entries {
		// skip excluded files/directories
		if j.shouldExclude(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("[DEBUG] SFTP: Failed to get info for root entry %s: %v", entry.Name(), err)
			continue
		}

		fileInfos = append(fileInfos, info)
	}

	log.Printf("[DEBUG] SFTP: Listed root directory with %d entries", len(fileInfos))
	return fileInfos, nil
}

// listerat implements the sftp.ListerAt interface
type listerat struct {
	entries []os.FileInfo
}

// ListAt returns the entries at the specified offset
func (l *listerat) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.entries)) {
		return 0, io.EOF
	}

	n := copy(ls, l.entries[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// virtualFileInfo implements os.FileInfo for virtual directory entries that don't physically
// exist in the filesystem but are needed for proper SFTP navigation. It's critical for:
// 1. Creating ".." (parent directory) entries required by SFTP protocol in every directory
// 2. Handling edge cases like presenting a single file as a directory listing
// 3. Ensuring compatibility with standard SFTP clients that expect these navigation aids
// Without these virtual entries, many SFTP clients would fail to navigate correctly.
type virtualFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (v *virtualFileInfo) Name() string       { return v.name }
func (v *virtualFileInfo) Size() int64        { return v.size }
func (v *virtualFileInfo) Mode() os.FileMode  { return v.mode }
func (v *virtualFileInfo) ModTime() time.Time { return v.modTime }
func (v *virtualFileInfo) IsDir() bool        { return v.isDir }
func (v *virtualFileInfo) Sys() interface{}   { return nil }

// securePath is a security-critical function that validates and normalizes filesystem paths
// from SFTP clients. It implements several security checks:
// 1. Normalizes paths to remove redundant components (like multiple slashes)
// 2. Explicitly checks for path traversal attempts using ".." components
// 3. Validates against the exclusion list to prevent access to sensitive files
// 4. Converts paths to the format required by fs.FS (no leading slash)
//
// The function is deliberately defensive with multiple layers of validation to prevent
// security bypasses. Even though fs.FS already handles some containment, this provides
// defense in depth against path traversal attacks.
func (j *jailedFilesystem) securePath(requestPath string) (string, error) {
	// handle root path specially
	if requestPath == "" || requestPath == "/" {
		return ".", nil
	}

	// remove leading slash for processing
	cleanPath := strings.TrimPrefix(requestPath, "/")

	// handle special cases
	if cleanPath == "" || cleanPath == "." {
		return ".", nil
	}

	// clean the path to handle redundant separators and . components
	cleanPath = filepath.Clean(cleanPath)

	// convert to slashes for consistent handling and use OS-specific
	// path for fs.FS operations
	osPath := filepath.FromSlash(cleanPath)

	// check for path traversal components
	// this is defense in depth, even though fs.FS should already protect against this
	// we use both the original request path and the cleaned path to catch both normalized and non-normalized attempts
	if strings.Contains(osPath, "..") || strings.Contains(requestPath, "..") {
		log.Printf("[WARN] SFTP: Path traversal attempt detected: %s", requestPath)
		return "", fmt.Errorf("path traversal attempt detected")
	}

	// check that this path isn't on the exclusion list
	if j.shouldExclude(osPath) {
		log.Printf("[DEBUG] SFTP: Path excluded by pattern: %s", osPath)
		return "", fmt.Errorf("path is excluded")
	}

	// the fs.FS will handle validation to ensure the path stays within the root,
	// so we don't need additional absolute path checks here

	return osPath, nil
}

// shouldExclude checks if a path should be excluded based on exclusion patterns
func (j *jailedFilesystem) shouldExclude(path string) bool {
	if len(j.excludes) == 0 {
		return false
	}

	// normalize path for matching
	normalizedPath := filepath.ToSlash(path)

	for _, pattern := range j.excludes {
		// convert pattern to forward slashes
		pattern = filepath.ToSlash(pattern)

		// exact match
		if normalizedPath == pattern {
			return true
		}

		// check if any path component matches the pattern
		parts := strings.Split(normalizedPath, "/")
		for _, part := range parts {
			if part == pattern {
				return true
			}
		}

		// check if path ends with the pattern
		if strings.HasSuffix(normalizedPath, "/"+pattern) {
			return true
		}
	}

	return false
}

// loadOrGenerateHostKey loads an existing SSH host key or generates a new one if it doesn't exist
func loadOrGenerateHostKey(keyFile string) (ssh.Signer, error) {
	// basic validation - just make sure the path isn't empty
	if keyFile == "" {
		return nil, fmt.Errorf("empty key file path")
	}

	// check if the key file exists
	// #nosec G304 - keyFile is validated above and controlled by the application config
	keyData, err := os.ReadFile(keyFile)
	if err == nil {
		// key exists, try to parse it
		hostKey, err := ssh.ParsePrivateKey(keyData)
		if err == nil {
			log.Printf("[INFO] Using existing SSH host key from %s", keyFile)
			return hostKey, nil
		}
		log.Printf("[WARN] Failed to parse existing host key: %v", err)
	}

	// key doesn't exist or couldn't be parsed, generate a new one
	log.Printf("[INFO] Generating new SSH host key and saving to %s", keyFile)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	keyData = pem.EncodeToMemory(pemBlock)

	// save the key to the file
	// #nosec G304 - keyFile is validated above and controlled by the application config
	if err := os.WriteFile(keyFile, keyData, 0600); err != nil {
		log.Printf("[WARN] Could not save SSH host key to %s: %v", keyFile, err)
	}

	// parse and return the newly generated key
	hostKey, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated host key: %w", err)
	}

	return hostKey, nil
}

// timeoutConn wraps a net.Conn with an idle timeout
type timeoutConn struct {
	net.Conn
	idleTimeout  time.Duration
	lastActivity time.Time
	mu           sync.Mutex
}

// Read wraps the underlying Read method and updates lastActivity
func (c *timeoutConn) Read(b []byte) (int, error) {
	// first check for timeout with read lock
	c.mu.Lock()
	lastAct := c.lastActivity
	c.mu.Unlock()

	if time.Since(lastAct) > c.idleTimeout {
		return 0, fmt.Errorf("idle timeout exceeded")
	}

	// read the data
	n, err := c.Conn.Read(b)

	// then update the activity time with write lock
	c.mu.Lock()
	c.lastActivity = time.Now()
	c.mu.Unlock()

	return n, err
}

// Write wraps the underlying Write method and updates lastActivity
func (c *timeoutConn) Write(b []byte) (int, error) {
	// first check for timeout with read lock
	c.mu.Lock()
	lastAct := c.lastActivity
	c.mu.Unlock()

	if time.Since(lastAct) > c.idleTimeout {
		return 0, fmt.Errorf("idle timeout exceeded")
	}

	// write the data
	n, err := c.Conn.Write(b)

	// then update the activity time with write lock
	c.mu.Lock()
	c.lastActivity = time.Now()
	c.mu.Unlock()

	return n, err
}

// bufferedFileReaderAt implements io.ReaderAt for large files
// It keeps the file open and performs buffered reads as needed
type bufferedFileReaderAt struct {
	file     fs.File
	fileSize int64
	path     string
	mu       sync.Mutex // protect concurrent reads
}

// ReadAt implements io.ReaderAt for large files
func (b *bufferedFileReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= b.fileSize {
		return 0, io.EOF
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// check if the file implements Seek
	seeker, ok := b.file.(io.Seeker)
	if !ok {
		return 0, fmt.Errorf("file does not support seeking")
	}

	// seek to the requested offset
	_, err = seeker.Seek(off, io.SeekStart)
	if err != nil {
		log.Printf("[ERROR] SFTP: Failed to seek in file %s: %v", b.path, err)
		return 0, err
	}

	// read data from the file
	n, err = io.ReadFull(b.file, p)

	// handle EOF correctly for ReadAt semantics
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n > 0 {
			// partial read at end of file is fine for ReadAt
			if off+int64(n) >= b.fileSize {
				return n, io.EOF
			}
			return n, nil
		}
		return 0, io.EOF
	}

	return n, err
}

// Close implements io.Closer for cleanup
func (b *bufferedFileReaderAt) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.file.Close()
}

// memReaderAt implements io.ReaderAt for a byte slice
type memReaderAt struct {
	data []byte
}

// ReadAt implements io.ReaderAt
func (m *memReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}

	n = copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// validateConfig validates the SFTP server configuration
func (s *SFTP) validateConfig() error {
	if s.SFTPUser == "" {
		return fmt.Errorf("SFTP username is required")
	}

	// validate authentication - either password or authorized keys must be provided
	if s.Auth == "" && s.SFTPAuthorized == "" {
		return fmt.Errorf("either password (--auth) or authorized keys file (--sftp-authorized) is required for SFTP server")
	}

	return nil
}

// setupPublicKeyAuth configures public key authentication for the SSH server
func (s *SFTP) setupPublicKeyAuth(config *ssh.ServerConfig) {
	authKeys, err := loadAuthorizedKeys(s.SFTPAuthorized)
	if err != nil {
		log.Printf("[WARN] Failed to load authorized keys from %s: %v", s.SFTPAuthorized, err)
		return
	}

	log.Printf("[INFO] Loaded %d authorized keys for public key authentication", len(authKeys))
	config.PublicKeyCallback = func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
		if subtle.ConstantTimeCompare([]byte(c.User()), []byte(s.SFTPUser)) != 1 {
			return nil, fmt.Errorf("unknown user %s", c.User())
		}

		// check if the public key is in the authorized keys
		pubKeyStr := string(ssh.MarshalAuthorizedKey(pubKey))
		for _, authorizedKey := range authKeys {
			authKeyStr := string(ssh.MarshalAuthorizedKey(authorizedKey))
			if pubKeyStr == authKeyStr {
				log.Printf("[DEBUG] Public key authentication successful for %s from %s", c.User(), c.RemoteAddr())
				return &ssh.Permissions{}, nil
			}
		}

		log.Printf("[WARN] Public key authentication failed for %s from %s", c.User(), c.RemoteAddr())
		return nil, fmt.Errorf("unauthorized public key")
	}
}

// setupSSHServerConfig configures the SSH server
func (s *SFTP) setupSSHServerConfig() (*ssh.ServerConfig, error) {
	// initialize the attempts tracking map
	s.ipAttempts = make(map[string]ipAttemptsInfo)

	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			// apply rate limiting based on source IP
			remoteIP := c.RemoteAddr().(*net.TCPAddr).IP.String()
			if !s.checkAuthRateLimit(remoteIP) {
				log.Printf("[WARN] SFTP rate limit exceeded for IP %s", remoteIP)
				// adding a small delay to slow down brute force attempts
				time.Sleep(2 * time.Second)
				return nil, fmt.Errorf("too many authentication attempts")
			}

			// if password auth is not enabled, reject
			if s.Auth == "" {
				log.Printf("[WARN] SFTP password authentication attempt when disabled for user %s from %s", c.User(), c.RemoteAddr())
				return nil, fmt.Errorf("password authentication disabled")
			}

			if subtle.ConstantTimeCompare([]byte(c.User()), []byte(s.SFTPUser)) == 1 && subtle.ConstantTimeCompare(pass, []byte(s.Auth)) == 1 {
				// successful login - clear rate limiting record
				s.resetAuthRateLimit(remoteIP)
				return &ssh.Permissions{}, nil
			}
			log.Printf("[WARN] SFTP password authentication failed for user %s from %s", c.User(), c.RemoteAddr())
			return nil, fmt.Errorf("authentication failed")
		},
		// set a custom server version string - helps hide implementation details
		ServerVersion: "SSH-2.0-WebList-SFTP",
		// set 10 minute idle timeout
		NoClientAuth: false,
		MaxAuthTries: 6,
	}

	// add public key authentication if authorized_keys file is provided
	if s.SFTPAuthorized != "" {
		s.setupPublicKeyAuth(config)
	}

	// try to load existing private key or generate a new one if it doesn't exist
	hostKey, err := loadOrGenerateHostKey(s.SFTPKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to setup host key: %w", err)
	}
	config.AddHostKey(hostKey)

	return config, nil
}

// checkAuthRateLimit implements a security mechanism to prevent brute-force attacks.
// It tracks authentication attempts by IP address with the following rules:
// 1. Maximum of 5 attempts allowed within a 10-minute sliding window
// 2. Window starts with the first authentication attempt
// 3. After 10 minutes of inactivity, the counter resets for that IP
// 4. Successful authentication will reset the counter immediately
//
// Returns true if the attempt is allowed, false if it exceeds the rate limit.
// This is a critical security feature to protect against password guessing.
func (s *SFTP) checkAuthRateLimit(remoteIP string) bool {
	s.ipAttemptsMu.Lock()
	defer s.ipAttemptsMu.Unlock()

	now := time.Now()
	info, exists := s.ipAttempts[remoteIP]

	// if no previous attempts or window has expired, reset
	if !exists || now.Sub(info.firstSeen) > 10*time.Minute {
		s.ipAttempts[remoteIP] = ipAttemptsInfo{
			count:     1,
			firstSeen: now,
			lastSeen:  now,
		}
		return true
	}

	// update attempt record
	info.count++
	info.lastSeen = now
	s.ipAttempts[remoteIP] = info

	// allow max 5 attempts in a 10-minute window
	return info.count <= 5
}

// resetAuthRateLimit clears rate limiting data for an IP after successful auth
func (s *SFTP) resetAuthRateLimit(remoteIP string) {
	s.ipAttemptsMu.Lock()
	defer s.ipAttemptsMu.Unlock()
	delete(s.ipAttempts, remoteIP)
}

// loadAuthorizedKeys reads and parses an authorized_keys file
func loadAuthorizedKeys(authorizedKeysFile string) ([]ssh.PublicKey, error) {
	// basic validation - just make sure the path isn't empty
	if authorizedKeysFile == "" {
		return nil, fmt.Errorf("empty authorized keys file path")
	}

	// read the authorized_keys file
	// #nosec G304 - authorizedKeysFile is validated above and provided in the application config
	keyData, err := os.ReadFile(authorizedKeysFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read authorized keys file: %w", err)
	}

	var authorizedKeys []ssh.PublicKey

	// parse the authorized_keys file
	for len(keyData) > 0 {
		// parse one key from the file
		pubKey, comment, options, rest, err := ssh.ParseAuthorizedKey(keyData)
		if err != nil {
			// skip bad lines
			s := bytes.IndexByte(keyData, '\n')
			if s == -1 {
				break
			}
			keyData = keyData[s+1:]
			continue
		}

		// debug logging
		if comment != "" {
			log.Printf("[DEBUG] Loaded authorized key: %s", comment)
		}

		// ignore options for now - we might want to handle these in the future
		_ = options

		// add the key to our list
		authorizedKeys = append(authorizedKeys, pubKey)

		// move to the next line
		keyData = rest
	}

	return authorizedKeys, nil
}
