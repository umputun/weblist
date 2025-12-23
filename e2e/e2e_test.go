//go:build e2e

// Package e2e contains end-to-end tests for weblist web application.
// Tests are organized by feature:
//   - e2e_test.go: TestMain setup, helpers, basic tests (home, footer)
//   - auth_test.go: authentication tests (separate server instance)
//   - nav_test.go: navigation and breadcrumb tests
//   - sort_test.go: sorting tests
//   - view_test.go: modal/view and theme tests
//   - api_test.go: API, download, and error tests
package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	baseURL     = "http://localhost:18080"
	testDataDir = "testdata" // relative to e2e directory where tests run
)

var (
	pw        *playwright.Playwright
	browser   playwright.Browser // single browser instance, reused across tests
	serverCmd *exec.Cmd
)

func TestMain(m *testing.M) {
	// tests run from e2e directory, so project root is parent
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("failed to get working directory: %v\n", err)
		os.Exit(1)
	}
	projectRoot := filepath.Dir(cwd)

	// get absolute path for testdata directory
	absTestData := filepath.Join(cwd, testDataDir)

	// build test binary
	build := exec.Command("go", "build", "-o", "/tmp/weblist-e2e", ".")
	build.Dir = projectRoot
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Printf("failed to build: %v\n", err)
		os.Exit(1)
	}

	// start server with test config serving testdata directory
	serverCmd = exec.Command("/tmp/weblist-e2e",
		"--listen=:18080",
		"--root="+absTestData,
	)
	// suppress server output to keep test output clean
	serverCmd.Stdout = nil
	serverCmd.Stderr = nil
	if err := serverCmd.Start(); err != nil {
		fmt.Printf("failed to start server: %v\n", err)
		os.Exit(1)
	}

	// wait for server readiness
	if err := waitForServer(baseURL+"/ping", 30*time.Second); err != nil {
		fmt.Printf("server not ready: %v\n", err)
		_ = serverCmd.Process.Kill()
		os.Exit(1)
	}

	// install playwright browsers
	if err := playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
	}); err != nil {
		fmt.Printf("failed to install playwright: %v\n", err)
		_ = serverCmd.Process.Kill()
		os.Exit(1)
	}

	// start playwright
	pw, err = playwright.Run()
	if err != nil {
		fmt.Printf("failed to start playwright: %v\n", err)
		_ = serverCmd.Process.Kill()
		os.Exit(1)
	}

	// launch browser once (reused across all tests via contexts)
	headless := os.Getenv("E2E_HEADLESS") != "false"
	var slowMo float64
	if !headless {
		slowMo = 50 // slow down visible browser for easier observation
	}
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
		SlowMo:   playwright.Float(slowMo),
	})
	if err != nil {
		fmt.Printf("failed to launch browser: %v\n", err)
		_ = pw.Stop()
		_ = serverCmd.Process.Kill()
		os.Exit(1)
	}

	// run tests
	code := m.Run()

	// cleanup
	_ = browser.Close()
	_ = pw.Stop()
	_ = serverCmd.Process.Kill()
	_ = os.Remove("/tmp/weblist-e2e")

	os.Exit(code)
}

func waitForServer(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec // test url
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server not ready after %v", timeout)
}

func newPage(t *testing.T) playwright.Page {
	t.Helper()
	ctx, err := browser.NewContext() // new context per test (isolated cookies/storage)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctx.Close() })

	page, err := ctx.NewPage()
	require.NoError(t, err)
	return page
}

// waitVisible waits for locator to become visible
func waitVisible(t *testing.T, loc playwright.Locator) {
	t.Helper()
	require.NoError(t, loc.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
}

// waitHidden waits for locator to become hidden
func waitHidden(t *testing.T, loc playwright.Locator) {
	t.Helper()
	require.NoError(t, loc.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateHidden,
	}))
}

// --- basic tests ---

func TestHome_PageLoads(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	title, err := page.Title()
	require.NoError(t, err)
	assert.Contains(t, title, "weblist")
}

func TestHome_ShowsFiles(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// check that sample.txt is visible
	visible, err := page.Locator("text=sample.txt").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "sample.txt should be visible in file listing")
}

func TestHome_ShowsDirectories(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// wait for table to load
	waitVisible(t, page.Locator("table"))

	// check that subdir is visible
	visible, err := page.Locator("text=subdir").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "subdir directory should be visible")
}

// --- footer tests ---

func TestFooter_IsVisible(t *testing.T) {
	page := newPage(t)
	_, err := page.Goto(baseURL)
	require.NoError(t, err)

	// check that footer is visible
	visible, err := page.Locator("footer").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "footer should be visible")

	// check that footer contains weblist link
	footerText, err := page.Locator("footer").InnerText()
	require.NoError(t, err)
	assert.Contains(t, footerText, "weblist")
}
