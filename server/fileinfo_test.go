package server

import (
	"testing"
	"testing/fstest"

	"github.com/dustin/go-humanize"
	"github.com/stretchr/testify/assert"
)

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
		name        string
		filePath    string
		wantType    string
		wantIsText  bool
		wantIsHTML  bool
		wantIsPDF   bool
		wantIsImage bool
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotIsText, gotIsHTML, gotIsPDF, gotIsImage := DetermineContentType(tt.filePath)

			// some MIME types might have charset added, so check if it starts with the expected type
			switch {
			case tt.wantIsHTML:
				assert.Contains(t, gotType, "text/html")
			case tt.name == "pdf file":
				assert.Equal(t, tt.wantType, gotType)
			case tt.name == "image file":
				assert.Equal(t, tt.wantType, gotType)
			case tt.name == "jsx file" || tt.name == "tsx file":
				assert.Equal(t, tt.wantType, gotType)
			default:
				assert.Contains(t, gotType, tt.wantType)
			}

			assert.Equal(t, tt.wantIsText, gotIsText)
			assert.Equal(t, tt.wantIsHTML, gotIsHTML)
			assert.Equal(t, tt.wantIsPDF, gotIsPDF)
			assert.Equal(t, tt.wantIsImage, gotIsImage)
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
			name: "no extension",
			fileInfo: FileInfo{
				Name: "test",
			},
			want: false,
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
	elfBinary := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
	elfBinary = append(elfBinary, make([]byte, 504)...) // pad to 512 bytes

	// PNG magic bytes
	pngBinary := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	pngBinary = append(pngBinary, make([]byte, 504)...)

	// ZIP magic bytes
	zipBinary := []byte{'P', 'K', 0x03, 0x04}
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
		{name: "no extension not checked", fileInfo: FileInfo{Name: "README", Path: "README"}, content: elfBinary, wantBinary: false},
		{name: "empty file", fileInfo: FileInfo{Name: "empty.txt", Path: "empty.txt"}, content: []byte{}, wantBinary: false},
		{name: "file open error", fileInfo: FileInfo{Name: "missing.txt", Path: "nonexistent/missing.txt"}, content: nil, wantBinary: false},
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
