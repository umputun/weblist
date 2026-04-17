package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginRateLimit(t *testing.T) {
	// create a test server with authentication enabled
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{
			ListenAddr: ":0",
			Theme:      "light",
			RootDir:    testdataDir,
			Auth:       "testpassword",
			Title:      "Test Server",
		},
		FS: os.DirFS(testdataDir),
	}

	// create the router to test the rate limiter middleware
	router, err := srv.router()
	require.NoError(t, err)

	// simulate multiple login attempts from the same IP
	for i := range 10 {
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "wrongpassword")

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.168.1.1:1234"

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		// first few attempts should return 200 (login form with error)
		// after exceeding rate limit (5 attempts), should get 429 Too Many Requests
		if i < 5 {
			assert.NotEqual(t, http.StatusTooManyRequests, rr.Code, "Request %d should not be rate limited", i+1)
		} else {
			t.Logf("Request %d status: %d", i+1, rr.Code)
			if rr.Code == http.StatusTooManyRequests {
				// once we hit rate limit, test passed
				assert.Contains(t, rr.Body.String(), "Too many login attempts", "Rate limit message should be shown")
				return
			}
		}

		// add a small delay between requests
		time.Sleep(10 * time.Millisecond)
	}

	// if we made it here, we never got rate limited
	t.Fatal("Rate limit was never triggered after 10 login attempts")
}

func TestLoginOversizedRequest(t *testing.T) {
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{ListenAddr: ":0", Theme: "light", RootDir: testdataDir, Auth: "testpassword"},
		FS:     os.DirFS(testdataDir),
	}

	router, err := srv.router()
	require.NoError(t, err)

	// send a POST /login request larger than 1MB SizeLimit
	body := strings.Repeat("x", 2*1024*1024)
	req := httptest.NewRequest("POST", "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code, "oversized login POST should be rejected with 413")
}

func TestAuthentication(t *testing.T) {
	// create a server with authentication
	srv := &Web{
		Config: Config{
			RootDir: "testdata",
			Auth:    "testpassword",
		},
		FS: os.DirFS("testdata"),
	}

	// initialize templates for testing
	err := srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	t.Run("redirect to login page when not authenticated", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(srv.handleRoot))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))
	})

	t.Run("access allowed with basic auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.SetBasicAuth("weblist", "testpassword")

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "success", rr.Body.String())
		// check that cookie is set with a session token, not raw password
		cookie := rr.Header().Get("Set-Cookie")
		assert.Contains(t, cookie, "auth=")
		assert.NotContains(t, cookie, "auth=testpassword")
	})

	t.Run("access allowed with cookie", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")
		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		loginSubmitHandler := http.HandlerFunc(srv.handleLoginSubmit)
		loginSubmitHandler.ServeHTTP(rr, req)

		cookies := rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}
		require.NotNil(t, authCookie, "Auth cookie should be set")

		// verify the session token is valid
		assert.True(t, srv.validateSessionToken(authCookie.Value), "Session token should be valid")

		// test auth with the cookie
		req, err = http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.AddCookie(authCookie)

		// use a new response recorder
		rr = httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "success", rr.Body.String())
	})

	t.Run("access denied with wrong password", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		req.SetBasicAuth("weblist", "wrongpassword")

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(srv.handleRoot))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))
	})

	t.Run("login page accessible without auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/login", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("login page"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "login page", rr.Body.String())
	})

	t.Run("assets accessible without auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/assets/css/custom.css", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("css content"))
		}))
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "css content", rr.Body.String())
	})

	t.Run("logout clears auth cookie", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/logout", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{
			Name:  "auth",
			Value: "testpassword",
		})

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLogout)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/login", rr.Header().Get("Location"))

		// check that the cookie is cleared
		cookies := rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}

		require.NotNil(t, authCookie, "Auth cookie should be present")
		assert.Equal(t, "", authCookie.Value, "Auth cookie value should be empty")
		assert.True(t, authCookie.MaxAge < 0, "Auth cookie MaxAge should be negative to delete it")
	})
}

func TestSessionTokens(t *testing.T) {
	srv := &Web{
		Config: Config{
			Auth:          "test-secret-password",
			SessionSecret: "test-session-secret",
		},
	}

	t.Run("token generation and validation", func(t *testing.T) {
		// generate a token
		token := srv.generateSessionToken()

		// token should have 3 parts separated by dots
		parts := strings.Split(token, ".")
		require.Len(t, parts, 3, "Session token should have 3 parts")

		// first part should be a UUID
		assert.Len(t, parts[0], 36, "First part should be a UUID")

		// second part should be a timestamp (number)
		_, err := strconv.ParseInt(parts[1], 10, 64)
		assert.NoError(t, err, "Second part should be a valid numeric timestamp")

		// third part should be a base64 encoded signature
		_, err = base64.StdEncoding.DecodeString(parts[2])
		assert.NoError(t, err, "Third part should be a valid base64 string")

		// token should validate
		assert.True(t, srv.validateSessionToken(token), "Token should validate successfully")
	})

	t.Run("token validation with wrong session secret", func(t *testing.T) {
		// generate a token with the initial session secret
		token := srv.generateSessionToken()

		// create a new server with a different session secret
		wrongSrv := &Web{
			Config: Config{
				Auth:          "test-secret-password",
				SessionSecret: "different-session-secret",
			},
		}

		// token should not validate with wrong session secret
		assert.False(t, wrongSrv.validateSessionToken(token), "Token should not validate with wrong session secret")
	})

	t.Run("invalid token format", func(t *testing.T) {
		// test with malformed tokens
		assert.False(t, srv.validateSessionToken("invalid"), "Invalid token should not validate")
		assert.False(t, srv.validateSessionToken("a.b.c"), "Malformed token should not validate")
		assert.False(t, srv.validateSessionToken(""), "Empty token should not validate")
	})

	t.Run("tampered token", func(t *testing.T) {
		// generate a valid token
		token := srv.generateSessionToken()
		parts := strings.Split(token, ".")

		// tamper with the token ID
		tamperedToken := "tampered-id." + parts[1] + "." + parts[2]
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered token should not validate")

		// tamper with the timestamp
		tamperedToken = parts[0] + ".9999999999." + parts[2]
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered timestamp should not validate")

		// tamper with the signature
		tamperedToken = parts[0] + "." + parts[1] + ".AAAA"
		assert.False(t, srv.validateSessionToken(tamperedToken), "Tampered signature should not validate")
	})
}

func TestHandleLoginSubmit(t *testing.T) {
	// create a test server with authentication enabled
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{
			ListenAddr: ":0",
			Theme:      "light",
			RootDir:    testdataDir,
			Auth:       "testpassword",
			Title:      "Test Server",
		},
		FS: os.DirFS(testdataDir),
	}

	// initialize templates for testing
	err = srv.initTemplates()
	require.NoError(t, err, "failed to initialize templates")

	t.Run("successful login", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "testpassword")

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusSeeOther, rr.Code)
		assert.Equal(t, "/", rr.Header().Get("Location"))

		cookies := rr.Result().Cookies()
		var authCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == "auth" {
				authCookie = cookie
				break
			}
		}
		require.NotNil(t, authCookie, "Auth cookie should be set")
		assert.True(t, srv.validateSessionToken(authCookie.Value), "Session token should be valid")
	})

	t.Run("failed login - wrong username", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("username", "wronguser")
		formData.Set("password", "testpassword")

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid username or password")
	})

	t.Run("failed login - wrong password", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("username", "weblist")
		formData.Set("password", "wrongpassword")

		req, err := http.NewRequest("POST", "/login", strings.NewReader(formData.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "Invalid username or password")
	})

	t.Run("form parsing error", func(t *testing.T) {
		req, err := http.NewRequest("POST", "/login", strings.NewReader("%"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(srv.handleLoginSubmit)
		handler.ServeHTTP(rr, req)

		// should return a bad request error
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "Failed to parse form")
	})

}

func TestHandleLoginPage(t *testing.T) {
	srv := setupTestServer(t)
	srv.BrandName = "Test Brand"
	srv.BrandColor = "ff0000"

	// create a request to pass to our handler
	req, err := http.NewRequest("GET", "/login", nil)
	require.NoError(t, err)

	// create a ResponseRecorder to record the response
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(srv.handleLoginPage)

	// call the handler
	handler.ServeHTTP(rr, req)

	// check the response status code is 200 OK
	assert.Equal(t, http.StatusOK, rr.Code)

	// check that the response contains expected elements
	responseBody := rr.Body.String()
	assert.Contains(t, responseBody, "Test Brand")
	assert.Contains(t, responseBody, "Test Title")
	assert.Contains(t, responseBody, "<form")
	assert.Contains(t, responseBody, "method=\"post\"")
	assert.Contains(t, responseBody, "action=\"/login\"")
}

func TestNormalizeBrandColor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty color", input: "", want: ""},
		{name: "already has hash prefix", input: "#ffffff", want: "#ffffff"},
		{name: "hex without hash prefix", input: "ff0000", want: "#ff0000"},
		{name: "named color", input: "blue", want: "#blue"},
	}

	srv := setupTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := srv.normalizeBrandColor(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCrossOriginProtection(t *testing.T) {
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)

	srv := &Web{
		Config: Config{ListenAddr: ":0", Theme: "light", RootDir: testdataDir, Auth: "testpassword", Title: "Test"},
		FS:     os.DirFS(testdataDir),
	}
	router, err := srv.router()
	require.NoError(t, err)

	tests := []struct {
		name           string
		method         string
		path           string
		secFetchSite   string
		origin         string
		host           string
		body           string
		contentType    string
		wantForbidden  bool
		wantStatusZero bool // when not forbidden, we don't care which non-403 code we get
	}{
		{name: "GET allowed without headers", method: "GET", path: "/login"},
		{name: "POST same-origin allowed", method: "POST", path: "/login", secFetchSite: "same-origin",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded"},
		{name: "POST none allowed (direct nav)", method: "POST", path: "/login", secFetchSite: "none",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded"},
		{name: "POST cross-site rejected", method: "POST", path: "/login", secFetchSite: "cross-site",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded",
			wantForbidden: true},
		{name: "POST same-site rejected (subdomain)", method: "POST", path: "/login", secFetchSite: "same-site",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded",
			wantForbidden: true},
		{name: "POST origin matches host allowed", method: "POST", path: "/login",
			origin: "http://example.com", host: "example.com",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded"},
		{name: "POST origin mismatch rejected", method: "POST", path: "/login",
			origin: "http://evil.com", host: "example.com",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded",
			wantForbidden: true},
		{name: "POST no headers (non-browser) allowed", method: "POST", path: "/login",
			body: "username=weblist&password=wrongpass", contentType: "application/x-www-form-urlencoded"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.NewReader(tt.body)
			req := httptest.NewRequest(tt.method, tt.path, body)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.host != "" {
				req.Host = tt.host
			}
			// unique source IP per case to avoid /login rate-limiter triggering across cases
			req.RemoteAddr = fmt.Sprintf("10.0.0.%d:1234", i+1)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if tt.wantForbidden {
				assert.Equal(t, http.StatusForbidden, rr.Code, "should be rejected as cross-origin")
				return
			}
			assert.NotEqual(t, http.StatusForbidden, rr.Code, "should not be rejected as cross-origin")
		})
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	srv := setupTestServer(t)

	// create a simple handler to wrap
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := srv.securityHeadersMiddleware(nextHandler)

	req, err := http.NewRequest("GET", "/", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// check security headers
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "SAMEORIGIN", rr.Header().Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", rr.Header().Get("X-XSS-Protection"))
	assert.Contains(t, rr.Header().Get("Content-Security-Policy"), "default-src 'self'")
	assert.Equal(t, "none", rr.Header().Get("X-Permitted-Cross-Domain-Policies"))
	assert.Equal(t, "noindex, nofollow", rr.Header().Get("X-Robots-Tag"))
}
