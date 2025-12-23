//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- file viewing tests ---

func TestView_FileModal(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for sample.txt
	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container"))

	// check that modal contains file name
	modalContent, err := page.Locator("#modal-container").InnerText()
	require.NoError(t, err)
	assert.Contains(t, modalContent, "sample.txt")
}

func TestView_ModalCloseOnEscape(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for sample.txt
	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container"))

	// press Escape to close modal
	require.NoError(t, page.Keyboard().Press("Escape"))

	// wait for modal content to be cleared
	assert.Eventually(t, func() bool {
		content, e := page.Locator("#modal-container").InnerHTML()
		return e == nil && content == ""
	}, 5*time.Second, 100*time.Millisecond, "modal should be closed after Escape")
}

func TestView_ModalCloseOnClickOutside(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for sample.txt
	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container .file-modal"))

	// click on the modal backdrop (outside the modal content)
	// the modal container covers the whole screen, clicking at position 10,10 hits the backdrop
	require.NoError(t, page.Locator("#modal-container").Click(playwright.LocatorClickOptions{
		Position: &playwright.Position{X: 10, Y: 10},
	}))

	// wait for modal content to be cleared
	assert.Eventually(t, func() bool {
		content, e := page.Locator("#modal-container").InnerHTML()
		return e == nil && content == ""
	}, 5*time.Second, 100*time.Millisecond, "modal should be closed after clicking outside")
}

func TestView_ModalCloseButton(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for sample.txt
	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container .file-modal"))

	// click on close button (X)
	require.NoError(t, page.Locator(".close-modal").Click())

	// wait for modal content to be cleared
	assert.Eventually(t, func() bool {
		content, e := page.Locator("#modal-container").InnerHTML()
		return e == nil && content == ""
	}, 5*time.Second, 100*time.Millisecond, "modal should be closed after clicking X button")
}

func TestView_ModalOpenInNewTab(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for sample.txt
	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container .file-modal"))

	// verify open-tab link exists and has correct href
	href, err := page.Locator(".open-tab").GetAttribute("href")
	require.NoError(t, err)
	assert.Contains(t, href, "/view/sample.txt")

	// verify it has target="_blank"
	target, err := page.Locator(".open-tab").GetAttribute("target")
	require.NoError(t, err)
	assert.Equal(t, "_blank", target)
}

func TestView_ImageInModal(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for test.png
	require.NoError(t, page.Locator("tr:has-text('test.png') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container .file-modal"))

	// verify image element exists in modal
	visible, err := page.Locator("#modal-container img.modal-image").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "image should be displayed in modal")
}

func TestView_HTMLInModal(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// click on view icon for test.html
	require.NoError(t, page.Locator("tr:has-text('test.html') .view-icon").Click())

	// wait for modal to appear
	waitVisible(t, page.Locator("#modal-container .file-modal"))

	// verify iframe exists in modal for HTML content
	visible, err := page.Locator("#modal-container iframe").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "iframe should be displayed for HTML files in modal")
}

func TestView_DirectFileAccess(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL + "/view/sample.txt")
	require.NoError(t, err)

	// should display file content page
	content, err := page.Locator("body").InnerText()
	require.NoError(t, err)
	assert.Contains(t, content, "sample text file")
}

func TestView_ViewIconOnlyForViewableFiles(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// sample.txt should have view icon (text file is viewable)
	viewIconCount, err := page.Locator("tr:has-text('sample.txt') .view-icon").Count()
	require.NoError(t, err)
	assert.Equal(t, 1, viewIconCount, "sample.txt should have view icon")

	// test.png should have view icon (image is viewable)
	viewIconCount, err = page.Locator("tr:has-text('test.png') .view-icon").Count()
	require.NoError(t, err)
	assert.Equal(t, 1, viewIconCount, "test.png should have view icon")
}

// --- theme tests ---

func TestTheme_DefaultIsLight(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// check data-theme attribute on html element
	theme, err := page.Locator("html").GetAttribute("data-theme")
	require.NoError(t, err)
	assert.Equal(t, "light", theme)
}

func TestTheme_ViewPageUsesServerDefault(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL + "/view/sample.txt")
	require.NoError(t, err)

	// check data-theme attribute on html element
	theme, err := page.Locator("html").GetAttribute("data-theme")
	require.NoError(t, err)
	assert.Equal(t, "light", theme)
}
