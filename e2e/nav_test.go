//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- navigation tests ---

func TestNav_ClickDirectory(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for initial table to load
	waitVisible(t, page.Locator("table"))

	// click on subdir directory row
	require.NoError(t, page.Locator("tr.dir-row:has-text('subdir')").Click())

	// wait for navigation to complete via HTMX (URL should contain path=subdir)
	require.NoError(t, page.WaitForURL("**/*path=subdir*"))

	// check that nested.txt is now visible
	waitVisible(t, page.Locator("text=nested.txt"))
}

func TestNav_BreadcrumbToHome(t *testing.T) {
	page := newPage(t)
	// navigate directly to subdir
	_, err := page.Goto(baseURL + "/?path=subdir")
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// verify we're in subdir (nested.txt should be visible)
	visible, err := page.Locator("text=nested.txt").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "should be in subdir showing nested.txt")

	// click on Home link in breadcrumbs
	require.NoError(t, page.Locator(".breadcrumbs a:has-text('Home')").Click())

	// wait for navigation to complete
	waitVisible(t, page.Locator("text=sample.txt"))
}

func TestNav_ParentDirectory(t *testing.T) {
	page := newPage(t)
	// navigate directly to subdir
	_, err := page.Goto(baseURL + "/?path=subdir")
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on parent directory (..)
	require.NoError(t, page.Locator("tr.dir-row:has-text('..')").Click())

	// wait for navigation to complete - sample.txt should be visible again
	waitVisible(t, page.Locator("text=sample.txt"))
}

// --- breadcrumb tests ---

func TestBreadcrumb_ShowsCurrentPath(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL + "/?path=subdir")
	require.NoError(t, err)

	// wait for page to load
	waitVisible(t, page.Locator("table"))

	// breadcrumbs should contain "subdir"
	breadcrumbText, err := page.Locator(".breadcrumbs").InnerText()
	require.NoError(t, err)
	assert.Contains(t, breadcrumbText, "subdir")
}

func TestBreadcrumb_PreservesSortParams(t *testing.T) {
	page := newPage(t)
	// navigate to subdir with sort params
	_, err := page.Goto(baseURL + "/?path=subdir&sort=size&dir=desc")
	require.NoError(t, err)

	// wait for page to load
	waitVisible(t, page.Locator("table"))

	// home link should include sort params
	homeHref, err := page.Locator(".breadcrumbs a:has-text('Home')").GetAttribute("hx-vals")
	require.NoError(t, err)
	assert.Contains(t, homeHref, "size")
}
