package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthLockout(t *testing.T) {
	var lk AuthLockout
	for i := range maxAuthFailures {
		require.True(t, lk.allow("198.51.100.1", false), "wrong attempt %d is honored", i+1)
	}
	assert.False(t, lk.allow("198.51.100.1", true), "right password refused once locked out")
	assert.True(t, lk.allow("198.51.100.2", false), "other addresses are unaffected")

	for range maxAuthFailures - 1 {
		lk.allow("198.51.100.3", false)
	}
	require.True(t, lk.allow("198.51.100.3", true))
	for i := range maxAuthFailures {
		require.True(t, lk.allow("198.51.100.3", false), "count restarts after a success, attempt %d", i+1)
	}
}

func TestAuthLockout_ExpireAfterWindow(t *testing.T) {
	// regression: the cache returns an expired entry's value, so a lockout never ended
	synctest.Test(t, func(t *testing.T) {
		var lk AuthLockout
		for range maxAuthFailures {
			lk.allow("198.51.100.1", false)
		}
		require.False(t, lk.allow("198.51.100.1", true))

		time.Sleep(authFailureWindow + time.Second)
		for i := range maxAuthFailures {
			require.True(t, lk.allow("198.51.100.1", false), "counting restarts from zero after expiry, attempt %d", i+1)
		}
		assert.False(t, lk.allow("198.51.100.1", true))
	})
}

func TestAuthLockout_IPv6SharesSlash64(t *testing.T) {
	var lk AuthLockout
	for i := range maxAuthFailures {
		lk.allow(fmt.Sprintf("2001:db8:1:2::%x", i+1), false)
	}
	assert.False(t, lk.allow("2001:db8:1:2:ffff::1", true), "addresses in one /64 share a counter")
	assert.True(t, lk.allow("2001:db8:1:3::1", true), "another /64 is unaffected")
}

func TestAuthLockout_ParallelGuessesStopAtLimit(t *testing.T) {
	// regression: the lockout check and the count took separate locks, so a burst passed the check at once
	var lk AuthLockout
	var honored atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if lk.allow("198.51.100.1", false) {
				honored.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(maxAuthFailures), honored.Load())
}

func newLimitTestServer(t *testing.T) http.Handler {
	t.Helper()
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	srv := &Web{Config: Config{RootDir: testdataDir, Auth: "right-pass", Theme: "light"}, FS: os.DirFS(testdataDir)}
	router, err := srv.router()
	require.NoError(t, err)
	return router
}

func basicAuthRequest(path, pass, peer string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	req.SetBasicAuth("weblist", pass)
	req.RemoteAddr = peer
	return req
}

func TestRouter_BasicAuthFailuresLockOutAcrossPaths(t *testing.T) {
	router := newLimitTestServer(t)

	for i := range maxAuthFailures {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicAuthRequest("/unique-"+strings.Repeat("x", i), "wrong", "203.0.113.9:4000"))
		require.Equal(t, http.StatusSeeOther, rr.Code, "attempt %d", i+1)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, basicAuthRequest("/another", "right-pass", "203.0.113.9:5000"))
	assert.Equal(t, http.StatusTooManyRequests, rr.Code, "locked out even with the right password, from a new port")

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, basicAuthRequest("/", "right-pass", "203.0.113.10:4000"))
	assert.Equal(t, http.StatusOK, rr.Code, "another address is not locked out")
}

func TestRouter_SuccessfulBasicAuthIsNeverCounted(t *testing.T) {
	router := newLimitTestServer(t)
	for i := range maxAuthFailures * 3 {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicAuthRequest("/", "right-pass", "203.0.113.9:4000"))
		require.Equal(t, http.StatusOK, rr.Code, "request %d", i+1)
	}
}

func TestRouter_ProxyBasicAuthIsNotCounted(t *testing.T) {
	// regression: a proxy's own Basic Auth header counted as a failure on every request
	router := newLimitTestServer(t)
	for i := range maxAuthFailures * 2 {
		req := httptest.NewRequest(http.MethodGet, "/login", http.NoBody)
		req.SetBasicAuth("proxy-user", "proxy-pass")
		req.RemoteAddr = "203.0.113.9:4000"
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "request %d", i+1)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, basicAuthRequest("/", "right-pass", "203.0.113.9:4000"))
	assert.Equal(t, http.StatusOK, rr.Code, "the address is not locked out")
}

func TestRouter_ProxyBasicAuthWithWeblistUserAllowsFormLogin(t *testing.T) {
	// regression: the counter ran router-wide, so the login page and its assets used up the
	// failures before the form could be submitted
	router := newLimitTestServer(t)
	for _, path := range []string{"/", "/login", "/assets/favicon.png", "/assets/css/custom.css", "/assets/css/weblist-app.css"} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicAuthRequest(path, "proxy-pass", "203.0.113.9:4000"))
		require.NotEqual(t, http.StatusTooManyRequests, rr.Code, path)
	}

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"password": {"right-pass"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("weblist", "proxy-pass")
	req.RemoteAddr = "203.0.113.9:4000"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusSeeOther, rr.Code)
	assert.NotEmpty(t, rr.Result().Cookies(), "form login issues a session")
}

func TestRouter_PublicReadIgnoresProxyBasicAuthOnReads(t *testing.T) {
	// regression: under public-read every page load with a proxy's weblist-user header counted as a failure
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	srv := &Web{Config: Config{RootDir: testdataDir, Auth: "right-pass", Theme: "light", EnableUpload: true, PublicRead: true},
		FS: os.DirFS(testdataDir)}
	router, err := srv.router()
	require.NoError(t, err)

	for i := range maxAuthFailures * 2 {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicAuthRequest("/", "proxy-pass", "203.0.113.9:4000"))
		require.Equal(t, http.StatusOK, rr.Code, "request %d", i+1)
	}

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"password": {"right-pass"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("weblist", "proxy-pass")
	req.RemoteAddr = "203.0.113.9:4000"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusSeeOther, rr.Code, "form login still works")
}

func TestRouter_CookieSessionIgnoresBasicAuthHeader(t *testing.T) {
	router := newLimitTestServer(t)
	srv := &Web{Config: Config{Auth: "right-pass"}}
	cookie := &http.Cookie{Name: "auth", Value: srv.generateSessionToken()}
	for i := range maxAuthFailures * 2 {
		req := basicAuthRequest("/", "proxy-pass", "203.0.113.9:4000")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "request %d", i+1)
	}
}

func TestRouter_LoginFailuresLockOut(t *testing.T) {
	router := newLimitTestServer(t)
	login := func(pass string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"password": {pass}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.9:4000"
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	for i := range maxAuthFailures {
		require.Equal(t, http.StatusOK, login("wrong").Code, "attempt %d", i+1)
	}
	rr := login("right-pass")
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Contains(t, rr.Body.String(), "Too many login attempts")
	assert.Empty(t, rr.Result().Cookies(), "no session for a locked-out address")
}

func TestRouter_GlobalLimitIgnoresPath(t *testing.T) {
	router := newLimitTestServer(t)
	limited := 0
	for i := range 80 {
		req := httptest.NewRequest(http.MethodGet, "/p"+strings.Repeat("y", i), http.NoBody)
		req.RemoteAddr = "203.0.113.9:4000"
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	assert.Positive(t, limited, "unique paths must share one per-address bucket")
}
