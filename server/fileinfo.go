package server

import (
	"fmt"
	"time"
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

	const unit = 1024
	if f.Size < unit {
		return fmt.Sprintf("%d B", f.Size)
	}

	div, exp := int64(unit), 0
	for n := f.Size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(f.Size)/float64(div), "KMGTPE"[exp])
}

// TimeString formats the last modified time
func (f FileInfo) TimeString() string {
	return f.LastModified.Format("02-Jan-2006 15:04:05")
}
