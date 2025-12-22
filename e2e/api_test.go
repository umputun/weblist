//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- file download tests ---

func TestDownload_FileLink(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// check that file link exists and has correct href
	href, err := page.Locator("tr:has-text('sample.txt') .file-link").GetAttribute("href")
	require.NoError(t, err)
	assert.Equal(t, "/sample.txt", href)
}

// --- API tests ---

func TestAPI_ListFiles(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/list") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	assert.Contains(t, string(body[:n]), "sample.txt")
	assert.Contains(t, string(body[:n]), "subdir")
}

func TestAPI_ListSubdir(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/list?path=subdir") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	assert.Contains(t, string(body[:n]), "nested.txt")
}

func TestAPI_ListWithSort(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/list?sort=-size") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	// response should include sort info
	assert.Contains(t, string(body[:n]), `"sort"`)
	assert.Contains(t, string(body[:n]), `"dir"`)
}

// --- error handling tests ---

func TestError_NotFoundFile(t *testing.T) {
	resp, err := http.Get(baseURL + "/nonexistent-file.txt") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestError_NotFoundDirectory(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/list?path=nonexistent") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestError_ViewDirectoryFails(t *testing.T) {
	resp, err := http.Get(baseURL + "/view/subdir") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	// should return bad request when trying to view a directory
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
