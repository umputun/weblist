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

// GetCommonTextExtensions returns a map of common text file extensions
func GetCommonTextExtensions() map[string]bool {
	return map[string]bool{
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
}

// DetermineContentType analyzes a file to determine its content type and common format flags.
// It uses a multi-step detection process:
// 1. Checks against a predefined list of known text file extensions
// 2. Falls back to standard MIME type detection based on file extension
// 3. Defaults to text/plain if no type could be determined
// 
// Returns:
// - contentType: The MIME type string for the file
// - isText: True for any text-based content (plain text, code, HTML, JSON, XML)
// - isHTML: True specifically for HTML content
// - isPDF: True for PDF documents
// - isImage: True for all image formats
//
// This is used for deciding how to present files in the UI (view vs. download).
func DetermineContentType(filePath string) (contentType string, isText, isHTML, isPDF, isImage bool) {
	ext := filepath.Ext(filePath)
	extLower := strings.ToLower(ext)
	commonTextExtensions := GetCommonTextExtensions()

	if commonTextExtensions[extLower] {
		contentType = "text/plain"
	} else {
		contentType = mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "text/plain"
		}
	}

	isText = strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/xml") ||
		strings.Contains(contentType, "html") ||
		commonTextExtensions[extLower]
	isHTML = strings.Contains(contentType, "html")
	isPDF = contentType == "application/pdf"
	isImage = strings.HasPrefix(contentType, "image/")

	return contentType, isText, isHTML, isPDF, isImage
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
	if GetCommonTextExtensions()[extLower] {
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
