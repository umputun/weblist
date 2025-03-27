package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
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
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP represents the SFTP server.
type SFTP struct {
	Config
	FS fs.FS
}

// Run starts the SFTP server.
func (s *SFTP) Run(ctx context.Context) error {
	// validate required fields
	if s.SFTPUser == "" {
		return fmt.Errorf("SFTP username is required")
	}
	if s.Auth == "" {
		return fmt.Errorf("password is required for SFTP server")
	}

	// configure SSH server
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == s.SFTPUser && string(pass) == s.Auth {
				return &ssh.Permissions{}, nil
			}
			log.Printf("[WARN] SFTP authentication failed for user %s from %s", c.User(), c.RemoteAddr())
			return nil, fmt.Errorf("authentication failed")
		},
	}

	// try to load existing private key or generate a new one
	hostKey, err := loadOrGenerateHostKey(s.SFTPKeyFile)
	if err != nil {
		return fmt.Errorf("failed to setup host key: %w", err)
	}
	config.AddHostKey(hostKey)

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

	// perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
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

// jailedFilesystem implements sftp.Handlers to restrict access to the root directory
type jailedFilesystem struct {
	rootDir  string   // physical root directory path
	excludes []string // patterns to exclude
	fsys     fs.FS    // filesystem interface
}

// Fileread implements sftp.FileCmder.Fileread
func (j *jailedFilesystem) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	// validate and secure the path
	secPath, err := j.securePath(r.Filepath)
	if err != nil {
		log.Printf("[WARN] SFTP: Denied file read access to %s: %v", r.Filepath, err)
		return nil, fmt.Errorf("access denied: %w", err)
	}

	// open file through fs.FS interface
	file, err := j.fsys.Open(secPath)
	if err != nil {
		log.Printf("[DEBUG] SFTP: Failed to open %s (secure path: %s): %v", r.Filepath, secPath, err)
		return nil, err
	}

	// check if file implements ReaderAt directly
	if ra, ok := file.(io.ReaderAt); ok {
		log.Printf("[DEBUG] SFTP: Allowed read access to %s (secure path: %s) using native ReaderAt", r.Filepath, secPath)
		return ra, nil
	}

	// otherwise, read the entire file into memory and create a memory-based ReaderAt
	data, err := io.ReadAll(file)
	if err := file.Close(); err != nil {
		log.Printf("[DEBUG] SFTP: Error closing file %s: %v", secPath, err)
	}

	if err != nil {
		log.Printf("[DEBUG] SFTP: Error reading file %s: %v", secPath, err)
		return nil, err
	}

	log.Printf("[DEBUG] SFTP: Allowed read access to %s (secure path: %s) using memory ReaderAt", r.Filepath, secPath)
	return &memReaderAt{data: data}, nil
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

	// verify it's a directory
	dirInfo, err := fs.Stat(j.fsys, secPath)
	if err != nil {
		log.Printf("[DEBUG] SFTP: Failed to stat directory %s (secure path: %s): %v", path, secPath, err)
		return nil, err
	}

	if !dirInfo.IsDir() {
		log.Printf("[WARN] SFTP: Attempted to list a non-directory: %s", path)
		return nil, fmt.Errorf("not a directory: %s", path)
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

// securePath validates and normalizes a path for use with fs.FS
// It returns the path relative to the fs.FS root (no leading slash)
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

// virtualFileInfo implements os.FileInfo for virtual directory entries
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

// loadOrGenerateHostKey loads an existing SSH host key or generates a new one if it doesn't exist
func loadOrGenerateHostKey(keyFile string) (ssh.Signer, error) {
	// basic validation - just make sure the path isn't empty
	if keyFile == "" {
		return nil, fmt.Errorf("empty key file path")
	}

	// #nosec G304 - path is provided by the user and validated above
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
