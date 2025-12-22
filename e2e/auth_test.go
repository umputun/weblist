//go:build e2e

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

// auth tests run on a separate server instance to avoid rate limiter conflicts
const (
	authBaseURL  = "http://localhost:18081"
	testPassword = "testpass123"
)

// startAuthServer starts a separate server instance with authentication enabled
func startAuthServer(t *testing.T) func() {
	t.Helper()

	// tests run from e2e directory, so project root is parent
	cwd, err := os.Getwd()
	require.NoError(t, err)
	absTestData := filepath.Join(cwd, testDataDir)

	cmd := exec.Command("/tmp/weblist-e2e",
		"--listen=:18081",
		"--root="+absTestData,
		"--auth="+testPassword,
		"--insecure-cookies", // needed for http in tests
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	require.NoError(t, cmd.Start())

	// wait for server to be ready
	err = waitForAuthServer(authBaseURL+"/ping", 10*time.Second)
	require.NoError(t, err, "auth server not ready")

	return func() {
		_ = cmd.Process.Kill()
	}
}

func waitForAuthServer(url string, timeout time.Duration) error {
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
	return fmt.Errorf("auth server not ready after %v", timeout)
}

// --- authentication tests ---

func TestAuth_LoginPageShown(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(authBaseURL)
	require.NoError(t, err)

	// should redirect to login page
	require.NoError(t, page.WaitForURL("**/login"))

	// check login form is visible - note: username is hidden, only password is visible
	visible, err := page.Locator("input[name='password']").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "password input should be visible")

	visible, err = page.Locator("button[type='submit']").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "submit button should be visible")

	// verify hidden username field exists with value "weblist"
	usernameValue, err := page.Locator("input[name='username']").GetAttribute("value")
	require.NoError(t, err)
	assert.Equal(t, "weblist", usernameValue)
}

func TestAuth_LoginValid(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(authBaseURL + "/login")
	require.NoError(t, err)

	// wait for login form - only password field is visible
	require.NoError(t, page.Locator("input[name='password']").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// fill in password (username is pre-filled as hidden field)
	require.NoError(t, page.Locator("input[name='password']").Fill(testPassword))

	// submit form
	require.NoError(t, page.Locator("button[type='submit']").Click())

	// should redirect to home page after successful login
	require.NoError(t, page.WaitForURL(authBaseURL+"/"))

	// verify file listing is visible (indicating successful auth)
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// verify logout link is visible
	visible, err := page.Locator("a[href='/logout']").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "logout link should be visible after login")
}

func TestAuth_LoginInvalid(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	page := newPage(t)
	_, err := page.Goto(authBaseURL + "/login")
	require.NoError(t, err)

	// wait for login form
	require.NoError(t, page.Locator("input[name='password']").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// fill in wrong password
	require.NoError(t, page.Locator("input[name='password']").Fill("wrongpassword"))

	// submit form
	require.NoError(t, page.Locator("button[type='submit']").Click())

	// wait for page to respond
	time.Sleep(500 * time.Millisecond)

	// should still be on login page (not redirected to home)
	url := page.URL()
	assert.Contains(t, url, "/login", "should remain on login page after invalid credentials")

	// login form should still be visible
	visible, err := page.Locator("input[name='password']").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "password input should still be visible after failed login")
}

func TestAuth_Logout(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	page := newPage(t)

	// first login
	_, err := page.Goto(authBaseURL + "/login")
	require.NoError(t, err)

	require.NoError(t, page.Locator("input[name='password']").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	require.NoError(t, page.Locator("input[name='password']").Fill(testPassword))
	require.NoError(t, page.Locator("button[type='submit']").Click())

	// wait for redirect to home
	require.NoError(t, page.WaitForURL(authBaseURL+"/"))

	// verify we're logged in
	require.NoError(t, page.Locator("table").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}))

	// click logout
	require.NoError(t, page.Locator("a[href='/logout']").Click())

	// should redirect to login page
	require.NoError(t, page.WaitForURL("**/login"))

	// verify login form is shown again
	visible, err := page.Locator("input[name='password']").IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "login form should be visible after logout")
}

func TestAuth_ProtectedRouteRequiresAuth(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	page := newPage(t)

	// try to access a protected route directly
	_, err := page.Goto(authBaseURL + "/?path=subdir")
	require.NoError(t, err)

	// should redirect to login
	require.NoError(t, page.WaitForURL("**/login"))
}

func TestAuth_APIRequiresAuth(t *testing.T) {
	cleanup := startAuthServer(t)
	defer cleanup()

	// use client that doesn't follow redirects
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// try to access API without auth
	resp, err := client.Get(authBaseURL + "/api/list") //nolint:gosec // test url
	require.NoError(t, err)
	defer resp.Body.Close()

	// should redirect (303 See Other) to login page
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "API should redirect to login without auth")
	assert.Contains(t, resp.Header.Get("Location"), "/login", "should redirect to login page")
}
