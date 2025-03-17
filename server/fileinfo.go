package server

import (
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
