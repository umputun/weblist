package server

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

// FileInfo represents a file or directory to be displayed in the list
type FileInfo struct {
	Name         string
	IsDir        bool
	Size         int64
	LastModified time.Time
	Path         string
	isBinary     bool // true if content detection indicates binary file despite text-like extension
}

// ContentTypeInfo holds content type information for a file
type ContentTypeInfo struct {
	MIMEType string // MIME type string for the file
	IsText   bool   // true for text-based content (plain text, code, HTML, JSON, XML)
	IsHTML   bool   // true specifically for HTML content
	IsPDF    bool   // true for PDF documents
	IsImage  bool   // true for all image formats
}

// SizeToString converts file size to human-readable format
func (f FileInfo) SizeToString() string {
	if f.IsDir {
		return "-"
	}

	// handle negative sizes (should not happen, but just in case)
	if f.Size < 0 {
		return "0B"
	}

	// safe conversion for positive sizes
	// #nosec G115 - we've checked that f.Size is not negative
	return humanize.Bytes(uint64(f.Size))
}

// TimeString formats the last modified time
func (f FileInfo) TimeString() string {
	return f.LastModified.Format("02-Jan-2006 15:04:05")
}

// TimeStringShort formats the last modified time with 2-digit year for mobile
func (f FileInfo) TimeStringShort() string {
	return f.LastModified.Format("02-Jan-06")
}

// isTextLikeMIME returns true if the MIME type represents text-like content that can be displayed.
func isTextLikeMIME(mimeType string) bool {
	return strings.HasPrefix(mimeType, "text/") ||
		strings.HasPrefix(mimeType, "application/json") ||
		strings.HasPrefix(mimeType, "application/xml") ||
		strings.HasPrefix(mimeType, "application/javascript") ||
		strings.Contains(mimeType, "html")
}

// commonTextExtensions contains a map of common text file extensions
var commonTextExtensions = func() map[string]bool {
	exts := []string{
		"txt", "text", "log", "csv", "json", "xml", "css", "scss", "less",
		"js", "jsx", "ts", "tsx", "go", "py", "java", "c", "cpp", "h", "hpp", "rb",
		"php", "swift", "pl", "sh", "bash", "zsh", "yaml", "yml", "toml", "ini", "conf",
		"config", "env", "lock", "md", "markdown", "rst", "adoc", "asciidoc", "bat", "cmd",
		"ps1", "psm1", "r", "m", "mat", "sas", "sql", "vb", "vbs", "cs", "fs", "fsx",
		"dart", "kotlin", "scala", "groovy", "lua", "rust", "rs", "vue", "elm", "ex", "exs",
		"hs", "clj", "d", "jl", "nim", "svg", "graphql", "gql", "proto", "avro", "diff", "patch",
		"properties", "cfg", "htaccess", "gitignore", "dockerignore", "rtf", "sdoc",
	}

	res := make(map[string]bool, len(exts))
	for _, ext := range exts {
		res[strings.ToLower("."+ext)] = true
	}
	return res
}()

// DetermineContentType analyzes a file to determine its content type and common format flags.
// It uses a multi-step detection process:
// 1. Checks against a predefined list of known text file extensions
// 2. Falls back to standard MIME type detection based on file extension
// 3. Defaults to text/plain if no type could be determined
//
// This is used for deciding how to present files in the UI (view vs. download).
func DetermineContentType(filePath string) ContentTypeInfo {
	ext := filepath.Ext(filePath)
	extLower := strings.ToLower(ext)

	var mimeType string
	// determine content type based on extension
	switch {
	// special handling for React/JSX files
	case extLower == ".jsx" || extLower == ".tsx":
		mimeType = "application/javascript"
	// handle other known text extensions
	case commonTextExtensions[extLower]:
		mimeType = "text/plain"
	// for unknown extensions, try standard MIME type detection
	default:
		mimeType = mime.TypeByExtension(ext)
		if mimeType == "" {
			mimeType = "text/plain"
		}
	}

	return ContentTypeInfo{
		MIMEType: mimeType,
		IsText:   isTextLikeMIME(mimeType) || commonTextExtensions[extLower],
		IsHTML:   strings.Contains(mimeType, "html"),
		IsPDF:    mimeType == "application/pdf",
		IsImage:  strings.HasPrefix(mimeType, "image/"),
	}
}

// IsViewable checks if the file can be viewed in a browser.
// Returns false if content detection found binary data despite text-like extension.
func (f FileInfo) IsViewable() bool {
	if f.IsDir || f.isBinary {
		return false
	}

	ext := filepath.Ext(f.Name)
	if ext == "" {
		return false
	}

	// special handling for common text formats that might not have proper MIME types
	extLower := strings.ToLower(ext)
	if commonTextExtensions[extLower] {
		return true
	}

	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		return false
	}

	// check if it's a text file, image, HTML, PDF, JavaScript, or JSON/XML
	return isTextLikeMIME(mimeType) ||
		strings.HasPrefix(mimeType, "image/") ||
		mimeType == "application/pdf"
}

// detectBinaryContent checks file content to determine if it's binary despite having a viewable extension.
// only checks files with viewable extensions to avoid unnecessary I/O.
// uses http.DetectContentType which reads up to 512 bytes.
// sets and returns the isBinary field.
func (f *FileInfo) detectBinaryContent(fsys fs.FS) bool {
	if f.IsDir {
		return false
	}

	// only check files that would be considered viewable by extension
	ext := filepath.Ext(f.Name)
	if ext == "" {
		return false
	}

	extLower := strings.ToLower(ext)
	isViewableByExt := commonTextExtensions[extLower]
	if !isViewableByExt {
		isViewableByExt = isTextLikeMIME(mime.TypeByExtension(ext))
	}

	if !isViewableByExt {
		return false // no need to check content for non-viewable extensions
	}

	file, err := fsys.Open(f.Path)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false
	}
	if n == 0 {
		return false
	}

	contentType := http.DetectContentType(buf[:n])
	// mark as binary if content is NOT text-like (catches images, archives, executables, etc.)
	f.isBinary = !isTextLikeMIME(contentType)
	return f.isBinary
}
