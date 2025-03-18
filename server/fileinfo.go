package server

import (
	"mime"
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

// IsViewable checks if the file can be viewed in a browser
func (f FileInfo) IsViewable() bool {
	if f.IsDir {
		return false
	}

	ext := filepath.Ext(f.Name)
	if ext == "" {
		return false
	}

	// special handling for common text formats that might not have proper MIME types
	extLower := strings.ToLower(ext)
	commonTextExtensions := map[string]bool{
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

	if commonTextExtensions[extLower] {
		return true
	}

	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		return false
	}

	// check if it's a text file, image, HTML, or PDF
	return strings.HasPrefix(mimeType, "text/") ||
		strings.HasPrefix(mimeType, "image/") ||
		mimeType == "application/pdf" ||
		strings.HasPrefix(mimeType, "application/json") ||
		strings.HasPrefix(mimeType, "application/xml") ||
		strings.Contains(mimeType, "html")
}
