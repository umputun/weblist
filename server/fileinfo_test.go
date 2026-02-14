package server

import (
	"testing"
	"testing/fstest"

	"github.com/dustin/go-humanize"
	"github.com/stretchr/testify/assert"
)

func TestIsTextLikeMIME(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{"text/plain", true},
		{"text/html", true},
		{"text/css", true},
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/xml", true},
		{"application/javascript", true},
		{"text/html; charset=utf-8", true},
		{"application/octet-stream", false},
		{"image/png", false},
		{"image/jpeg", false},
		{"application/pdf", false},
		{"application/zip", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.mimeType, func(t *testing.T) {
			got := isTextLikeMIME(tc.mimeType)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSizeToString(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		isDir    bool
		expected string
	}{
		{
			name:     "bytes",
			size:     500,
			expected: "500B",
		},
		{
			name:     "kilobytes",
			size:     1500,
			expected: "1.5KB",
		},
		{
			name:     "megabytes",
			size:     1500000,
			expected: "1.4MB",
		},
		{
			name:     "gigabytes",
			size:     1500000000,
			expected: "1.4GB",
		},
		{
			name:     "terabytes",
			size:     1500000000000,
			expected: "1.4TB",
		},
		{
			name:     "zero",
			size:     0,
			expected: "0B",
		},
		{
			name:     "directory",
			size:     1000,
			isDir:    true,
			expected: "-",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fi := FileInfo{Size: tc.size, IsDir: tc.isDir}
			result := fi.SizeToString()

			if !tc.isDir {
				// for non-directory files, verify using the humanize library directly
				expected := humanize.Bytes(uint64(tc.size))
				assert.Equal(t, expected, result)
			} else {
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestTimeString(t *testing.T) {
	fi := FileInfo{}
	result := fi.TimeString()
	assert.NotEmpty(t, result)
}

func TestDetermineContentType(t *testing.T) {
	tests := []struct {
		name           string
		filePath       string
		wantType       string
		wantIsText     bool
		wantIsHTML     bool
		wantIsPDF      bool
		wantIsImage    bool
		wantIsMarkdown bool
	}{
		{
			name:        "plain text file",
			filePath:    "test.txt",
			wantType:    "text/plain",
			wantIsText:  true,
			wantIsHTML:  false,
			wantIsPDF:   false,
			wantIsImage: false,
		},
		{
			name:        "html file",
			filePath:    "test.html",
			wantType:    "text/html",
			wantIsText:  true,
			wantIsHTML:  true,
			wantIsPDF:   false,
			wantIsImage: false,
		},
		{
			name:        "pdf file",
			filePath:    "test.pdf",
			wantType:    "application/pdf",
			wantIsText:  false,
			wantIsHTML:  false,
			wantIsPDF:   true,
			wantIsImage: false,
		},
		{
			name:        "image file",
			filePath:    "test.png",
			wantType:    "image/png",
			wantIsText:  false,
			wantIsHTML:  false,
			wantIsPDF:   false,
			wantIsImage: true,
		},
		{
			name:        "jsx file",
			filePath:    "test.jsx",
			wantType:    "application/javascript",
			wantIsText:  true,
			wantIsHTML:  false,
			wantIsPDF:   false,
			wantIsImage: false,
		},
		{
			name:        "tsx file",
			filePath:    "test.tsx",
			wantType:    "application/javascript",
			wantIsText:  true,
			wantIsHTML:  false,
			wantIsPDF:   false,
			wantIsImage: false,
		},
		{
			name:        "javascript file",
			filePath:    "test.js",
			wantType:    "text/plain",
			wantIsText:  true,
			wantIsHTML:  false,
			wantIsPDF:   false,
			wantIsImage: false,
		},
		{
			name:           "markdown file .md",
			filePath:       "readme.md",
			wantType:       "text/plain",
			wantIsText:     true,
			wantIsMarkdown: true,
		},
		{
			name:           "markdown file .markdown",
			filePath:       "readme.markdown",
			wantType:       "text/plain",
			wantIsText:     true,
			wantIsMarkdown: true,
		},
		{
			name:       "plain text is not markdown",
			filePath:   "readme.txt",
			wantType:   "text/plain",
			wantIsText: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctInfo := DetermineContentType(tt.filePath)

			// some MIME types might have charset added, so check if it starts with the expected type
			switch {
			case tt.wantIsHTML:
				assert.Contains(t, ctInfo.MIMEType, "text/html")
			case tt.name == "pdf file":
				assert.Equal(t, tt.wantType, ctInfo.MIMEType)
			case tt.name == "image file":
				assert.Equal(t, tt.wantType, ctInfo.MIMEType)
			case tt.name == "jsx file" || tt.name == "tsx file":
				assert.Equal(t, tt.wantType, ctInfo.MIMEType)
			default:
				assert.Contains(t, ctInfo.MIMEType, tt.wantType)
			}

			assert.Equal(t, tt.wantIsText, ctInfo.IsText)
			assert.Equal(t, tt.wantIsHTML, ctInfo.IsHTML)
			assert.Equal(t, tt.wantIsPDF, ctInfo.IsPDF)
			assert.Equal(t, tt.wantIsImage, ctInfo.IsImage)
			assert.Equal(t, tt.wantIsMarkdown, ctInfo.IsMarkdown)
		})
	}
}

func TestFileInfo_IsViewable(t *testing.T) {
	tests := []struct {
		name     string
		fileInfo FileInfo
		want     bool
	}{
		{
			name: "directory",
			fileInfo: FileInfo{
				Name:  "test-dir",
				IsDir: true,
			},
			want: false,
		},
		{
			name: "text file",
			fileInfo: FileInfo{
				Name: "test.txt",
			},
			want: true,
		},
		{
			name: "html file",
			fileInfo: FileInfo{
				Name: "test.html",
			},
			want: true,
		},
		{
			name: "jsx file",
			fileInfo: FileInfo{
				Name: "test.jsx",
			},
			want: true,
		},
		{
			name: "tsx file",
			fileInfo: FileInfo{
				Name: "test.tsx",
			},
			want: true,
		},
		{
			name:     "unknown extensionless file detected as text",
			fileInfo: FileInfo{Name: "somefile", isBinary: false},
			want:     true,
		},
		{
			name:     "unknown extensionless file detected as binary",
			fileInfo: FileInfo{Name: "somefile", isBinary: true},
			want:     false,
		},
		{
			name:     "known text filename Makefile",
			fileInfo: FileInfo{Name: "Makefile", isBinary: false},
			want:     true,
		},
		{
			name:     "known text filename Dockerfile",
			fileInfo: FileInfo{Name: "Dockerfile", isBinary: false},
			want:     true,
		},
		{
			name:     "known text filename LICENSE",
			fileInfo: FileInfo{Name: "LICENSE", isBinary: false},
			want:     true,
		},
		{
			name:     "known text filename marked as binary",
			fileInfo: FileInfo{Name: "Makefile", isBinary: true},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fileInfo.IsViewable()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFileInfo_detectBinaryContent(t *testing.T) {
	// ELF binary magic bytes
	elfBinary := make([]byte, 0, 512)
	elfBinary = append(elfBinary, 0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00)
	elfBinary = append(elfBinary, make([]byte, 504)...) // pad to 512 bytes

	// PNG magic bytes
	pngBinary := make([]byte, 0, 512)
	pngBinary = append(pngBinary, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)
	pngBinary = append(pngBinary, make([]byte, 504)...)

	// ZIP magic bytes
	zipBinary := make([]byte, 0, 512)
	zipBinary = append(zipBinary, 'P', 'K', 0x03, 0x04)
	zipBinary = append(zipBinary, make([]byte, 508)...)

	tests := []struct {
		name       string
		fileInfo   FileInfo
		content    []byte
		wantBinary bool
	}{
		{name: "elf binary with jsx extension", fileInfo: FileInfo{Name: "app.jsx", Path: "app.jsx"}, content: elfBinary, wantBinary: true},
		{name: "elf binary with go extension", fileInfo: FileInfo{Name: "main.go", Path: "main.go"}, content: elfBinary, wantBinary: true},
		{name: "png with text extension", fileInfo: FileInfo{Name: "image.txt", Path: "image.txt"}, content: pngBinary, wantBinary: true},
		{name: "zip with text extension", fileInfo: FileInfo{Name: "archive.md", Path: "archive.md"}, content: zipBinary, wantBinary: true},
		{name: "text content with jsx extension", fileInfo: FileInfo{Name: "app.jsx", Path: "app.jsx"},
			content: []byte("import React from 'react';\nexport default function App() { return <div>Hello</div>; }"), wantBinary: false},
		{name: "text content with go extension", fileInfo: FileInfo{Name: "main.go", Path: "main.go"},
			content: []byte("package main\n\nfunc main() {}\n"), wantBinary: false},
		{name: "html file via mime fallback", fileInfo: FileInfo{Name: "page.html", Path: "page.html"},
			content: []byte("<!DOCTYPE html><html><body>Hello</body></html>"), wantBinary: false},
		{name: "html content with text extension", fileInfo: FileInfo{Name: "page.txt", Path: "page.txt"},
			content: []byte("<!DOCTYPE html><html><body>Hello</body></html>"), wantBinary: false},
		{name: "directory is not checked", fileInfo: FileInfo{Name: "somedir", Path: "somedir", IsDir: true}, content: nil, wantBinary: false},
		{name: "binary extension not checked", fileInfo: FileInfo{Name: "app.exe", Path: "app.exe"}, content: elfBinary, wantBinary: false},
		{name: "empty file", fileInfo: FileInfo{Name: "empty.txt", Path: "empty.txt"}, content: []byte{}, wantBinary: false},
		{name: "file open error", fileInfo: FileInfo{Name: "missing.txt", Path: "nonexistent/missing.txt"}, content: nil, wantBinary: false},

		// extensionless files - now checked for binary content
		{name: "extensionless with binary content", fileInfo: FileInfo{Name: "binary_data", Path: "binary_data"},
			content: elfBinary, wantBinary: true},
		{name: "extensionless Makefile with text content", fileInfo: FileInfo{Name: "Makefile", Path: "Makefile"},
			content: []byte(".PHONY: all\nall:\n\tgo build ./...\n"), wantBinary: false},
		{name: "extensionless Dockerfile with text content", fileInfo: FileInfo{Name: "Dockerfile", Path: "Dockerfile"},
			content: []byte("FROM golang:1.21\nWORKDIR /app\nCOPY . .\nRUN go build\n"), wantBinary: false},
		{name: "extensionless LICENSE with text content", fileInfo: FileInfo{Name: "LICENSE", Path: "LICENSE"},
			content: []byte("MIT License\n\nCopyright (c) 2024\n"), wantBinary: false},
		{name: "extensionless unknown with text content", fileInfo: FileInfo{Name: "somefile", Path: "somefile"},
			content: []byte("this is plain text content\nwith multiple lines\n"), wantBinary: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			if tt.content != nil {
				fsys[tt.fileInfo.Path] = &fstest.MapFile{Data: tt.content}
			}

			fi := tt.fileInfo
			got := fi.detectBinaryContent(fsys)
			assert.Equal(t, tt.wantBinary, got, "detectBinaryContent return mismatch")
			assert.Equal(t, tt.wantBinary, fi.isBinary, "isBinary field mismatch")

			if tt.wantBinary {
				assert.False(t, fi.IsViewable(), "binary file should not be viewable")
			}
		})
	}
}

func TestExtensionlessFileIntegration(t *testing.T) {
	// ELF binary magic bytes
	elfBinary := make([]byte, 0, 512)
	elfBinary = append(elfBinary, 0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00)
	elfBinary = append(elfBinary, make([]byte, 504)...)

	tests := []struct {
		name         string
		filename     string
		content      []byte
		wantViewable bool
	}{
		{name: "Makefile with text content", filename: "Makefile",
			content: []byte(".PHONY: build\nbuild:\n\tgo build\n"), wantViewable: true},
		{name: "Dockerfile with text content", filename: "Dockerfile",
			content: []byte("FROM alpine\nRUN echo hello\n"), wantViewable: true},
		{name: "LICENSE with text content", filename: "LICENSE",
			content: []byte("MIT License\nCopyright 2024\n"), wantViewable: true},
		{name: "unknown extensionless text file", filename: "myconfig",
			content: []byte("key=value\nother=setting\n"), wantViewable: true},
		{name: "unknown extensionless binary file", filename: "compiled",
			content: elfBinary, wantViewable: false},
		{name: "Makefile with binary content (corrupted)", filename: "Makefile",
			content: elfBinary, wantViewable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				tt.filename: &fstest.MapFile{Data: tt.content},
			}

			fi := FileInfo{Name: tt.filename, Path: tt.filename}
			fi.detectBinaryContent(fsys)

			assert.Equal(t, tt.wantViewable, fi.IsViewable(),
				"expected IsViewable=%v for %s (isBinary=%v)", tt.wantViewable, tt.filename, fi.isBinary)
		})
	}
}
