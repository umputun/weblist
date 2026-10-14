//go:build e2e

package e2e

import (
	"bytes"
	"image/png"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
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

func TestView_MarkdownRendered(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL + "/view/test.md")
	require.NoError(t, err)

	// verify rendered heading is present (not raw markdown)
	heading, err := page.Locator("h1").InnerText()
	require.NoError(t, err)
	assert.Equal(t, "Markdown Test", heading)

	// verify markdown-content wrapper is present
	visible, err := page.Locator(".markdown-content").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "markdown-content wrapper should be visible")

	// verify raw markdown is not shown
	body, err := page.Locator("body").InnerHTML()
	require.NoError(t, err)
	assert.NotContains(t, body, "# Markdown Test")

	// verify table rendered
	tableVisible, err := page.Locator(".markdown-content table").IsVisible()
	require.NoError(t, err)
	assert.True(t, tableVisible, "table should be rendered")
}

func TestView_CSVRenderedAsTable(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL + "/view/test.csv")
	require.NoError(t, err)

	// verify csv-content wrapper is present
	visible, err := page.Locator(".csv-content").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "csv-content wrapper should be visible")

	// verify table is rendered
	tableVisible, err := page.Locator(".csv-content table").IsVisible()
	require.NoError(t, err)
	assert.True(t, tableVisible, "table should be rendered")

	// verify header row
	headerText, err := page.Locator(".csv-content thead").InnerText()
	require.NoError(t, err)
	assert.Contains(t, headerText, "Name")
	assert.Contains(t, headerText, "Age")
	assert.Contains(t, headerText, "City")

	// verify data rows
	bodyText, err := page.Locator(".csv-content tbody").InnerText()
	require.NoError(t, err)
	assert.Contains(t, bodyText, "Alice")
	assert.Contains(t, bodyText, "Bob")
	assert.Contains(t, bodyText, "Charlie")

	// verify raw csv is not shown
	body, err := page.Locator("body").InnerHTML()
	require.NoError(t, err)
	assert.NotContains(t, body, "Name,Age,City")
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

func TestView_DarkModalTextPreviewIsNotWhiteWhileLoading(t *testing.T) {
	// regression: in the dark theme WebKit painted the text preview iframe white until /view responded
	wk, err := pw.WebKit.Launch(playwright.BrowserTypeLaunchOptions{Headless: new(os.Getenv("E2E_HEADLESS") != "false")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = wk.Close() })
	page, err := wk.NewPage()
	require.NoError(t, err)
	_, err = page.Goto(baseURL)
	require.NoError(t, err)
	waitVisible(t, page.Locator("table"))
	_, err = page.Evaluate(`() => document.documentElement.setAttribute('data-theme', 'dark')`)
	require.NoError(t, err)

	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	require.NoError(t, page.Route("**/view/**", func(route playwright.Route) {
		go func() {
			<-release
			_ = route.Continue()
		}()
	}))

	require.NoError(t, page.Locator("tr:has-text('sample.txt') .view-icon").Click())
	frame := page.Locator("#modal-container iframe.text-preview")
	require.NoError(t, frame.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached}))
	time.Sleep(300 * time.Millisecond)
	shot, err := frame.Screenshot()
	require.NoError(t, err)
	unblock()

	img, err := png.Decode(bytes.NewReader(shot))
	require.NoError(t, err)
	var sum, n float64
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
			n++
		}
	}
	assert.Less(t, sum/n, 128.0, "preview area should stay dark while the iframe loads")
}

func TestTheme_DarkViewPageDeclaresSchemeBeforeStylesheets(t *testing.T) {
	// regression: the dark preview iframe painted white until its stylesheets loaded
	page := newPage(t)
	require.NoError(t, page.Route("**/assets/css/*", func(route playwright.Route) {
		_ = route.Abort()
	}))
	_, err := page.Goto(baseURL + "/view/sample.txt?theme=dark")
	require.NoError(t, err)

	scheme, err := page.Evaluate(`() => getComputedStyle(document.documentElement).colorScheme`)
	require.NoError(t, err)
	assert.Equal(t, "dark", scheme)
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
