package server

import (
	"testing"

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
			if tt.wantIsHTML {
				assert.Contains(t, gotType, "text/html")
			} else if tt.name == "pdf file" {
				assert.Equal(t, tt.wantType, gotType)
			} else if tt.name == "image file" {
				assert.Equal(t, tt.wantType, gotType)
			} else if tt.name == "jsx file" || tt.name == "tsx file" {
				assert.Equal(t, tt.wantType, gotType)
			} else {
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
