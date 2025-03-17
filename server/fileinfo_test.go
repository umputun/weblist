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
