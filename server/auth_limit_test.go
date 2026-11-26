package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthFailures(t *testing.T) {
	var af authFailures
	for range maxAuthFailures - 1 {
		af.fail("198.51.100.1")
	}
	assert.False(t, af.blocked("198.51.100.1"))
	af.fail("198.51.100.1")
	assert.True(t, af.blocked("198.51.100.1"))
	assert.False(t, af.blocked("198.51.100.2"), "other addresses are unaffected")

	af.reset("198.51.100.1")
	assert.False(t, af.blocked("198.51.100.1"))
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
