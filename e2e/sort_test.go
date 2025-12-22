//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- sorting tests ---

func TestSort_ByName(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click on Name header to toggle sort
	require.NoError(t, page.Locator("th.name-cell").Click())

	// wait for reload via HTMX - URL should update with sort params
	require.NoError(t, page.WaitForURL("**/*sort=name*"))

	// table should still be visible after sort
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))
}

func TestSort_ByDate(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click on Last Modified header
	require.NoError(t, page.Locator("th.date-col").Click())

	// wait for reload via HTMX - URL should update with sort params
	require.NoError(t, page.WaitForURL("**/*sort=date*"))

	// verify sort arrow appears in date column header (↑ or ↓)
	headerText, err := page.Locator("th.date-col").InnerText()
	require.NoError(t, err)
	assert.True(t, strings.Contains(headerText, "↑") || strings.Contains(headerText, "↓"),
		"date column should have sort arrow indicator")
}

func TestSort_BySize(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click on Size header
	require.NoError(t, page.Locator("th.size-col").Click())

	// wait for reload via HTMX - URL should update with sort params
	require.NoError(t, page.WaitForURL("**/*sort=size*"))

	// verify sort arrow appears in size column header (↑ or ↓)
	headerText, err := page.Locator("th.size-col").InnerText()
	require.NoError(t, err)
	assert.True(t, strings.Contains(headerText, "↑") || strings.Contains(headerText, "↓"),
		"size column should have sort arrow indicator")
}

func TestSort_DirectionToggle(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click on Name header first time - should sort ascending
	require.NoError(t, page.Locator("th.name-cell").Click())
	require.NoError(t, page.WaitForURL("**/*sort=name*dir=desc*"))

	// verify arrow direction (should be descending on first click since default is asc)
	headerText, err := page.Locator("th.name-cell").InnerText()
	require.NoError(t, err)
	assert.Contains(t, headerText, "↓", "first click should show descending arrow")

	// click same header again - should toggle direction
	require.NoError(t, page.Locator("th.name-cell").Click())
	require.NoError(t, page.WaitForURL("**/*sort=name*dir=asc*"))

	headerText, err = page.Locator("th.name-cell").InnerText()
	require.NoError(t, err)
	assert.Contains(t, headerText, "↑", "second click should show ascending arrow")
}

func TestSort_PreservesPathOnSort(t *testing.T) {
	page := newPage(t)
	// navigate to subdir first
	_, err := page.Goto(baseURL + "/?path=subdir")
	require.NoError(t, err)

	// wait for table to load
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click on Name header to sort
	require.NoError(t, page.Locator("th.name-cell").Click())

	// URL should contain both path and sort params
	require.NoError(t, page.WaitForURL("**/*path=subdir*sort=name*"))

	// nested.txt should still be visible (we're still in subdir)
	visible, err := page.Locator("text=nested.txt").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "should remain in subdir after sorting")
}
