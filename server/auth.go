package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// loginTemplateData holds data for login page template rendering
type loginTemplateData struct {
	Theme        string
	HideFooter   bool
	Title        string
	Error        string
	BrandName    string
	BrandColor   string
	CustomFooter string
	CSRFToken    string
}

// handleLoginPage renders the login page
func (wb *Web) handleLoginPage(w http.ResponseWriter, r *http.Request) {

	// generate CSRF token
	csrfToken := wb.generateCSRFToken()

	// set CSRF token in a cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(5 * time.Minute.Seconds()), // CSRF token valid for 5 minutes
	})

	data := loginTemplateData{
		Theme:        wb.Theme,
		HideFooter:   wb.HideFooter,
		Title:        wb.Title,
		BrandName:    wb.BrandName,
		BrandColor:   wb.BrandColor,
		Error:        "",
		CustomFooter: wb.CustomFooter,
		CSRFToken:    csrfToken,
	}

	if err := wb.templates.loginTemplate.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("failed to execute template: %v", err), http.StatusInternalServerError)
		return
	}
}

// handleLoginSubmit handles the login form submission
func (wb *Web) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// verify CSRF token
	formToken := r.FormValue("csrf_token")
	cookieToken, err := r.Cookie("csrf_token")
	if err != nil || formToken == "" || subtle.ConstantTimeCompare([]byte(formToken), []byte(cookieToken.Value)) != 1 {
		wb.renderLoginError(w, r, "Invalid or missing CSRF token")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// check credentials
	usernameCorrect := subtle.ConstantTimeCompare([]byte(username), []byte(wb.getAuthUser())) == 1
	passwordCorrect := subtle.ConstantTimeCompare([]byte(password), []byte(wb.Auth)) == 1

	// authentication failed, show error
	if !usernameCorrect || !passwordCorrect {
		wb.renderLoginError(w, r, "Invalid username or password")
		return
	}

	// clear the CSRF token cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   -1, // delete the cookie
	})

	// authentication successful, generate session token and set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    wb.generateSessionToken(),
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   wb.getSessionMaxAge(),
	})

	// redirect to the home page
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderLoginError renders the login page with an error message
func (wb *Web) renderLoginError(w http.ResponseWriter, r *http.Request, errorMsg string) {

	// generate a new CSRF token
	csrfToken := wb.generateCSRFToken()

	// set CSRF token in cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(5 * time.Minute.Seconds()),
	})

	data := loginTemplateData{
		Theme:        wb.Theme,
		HideFooter:   wb.HideFooter,
		Title:        wb.Title,
		BrandName:    wb.BrandName,
		BrandColor:   wb.BrandColor,
		Error:        errorMsg,
		CustomFooter: wb.CustomFooter,
		CSRFToken:    csrfToken,
	}

	if err := wb.templates.loginTemplate.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("failed to execute template: %v", err), http.StatusInternalServerError)
	}
}

// handleLogout handles the logout request
func (wb *Web) handleLogout(w http.ResponseWriter, r *http.Request) {
	// clear the auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   -1, // delete the cookie
	})

	// redirect to the login page
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// isAuthenticatedByCookie checks if the user is authenticated via cookie
func (wb *Web) isAuthenticatedByCookie(r *http.Request) bool {
	cookie, err := r.Cookie("auth")
	if err != nil {
		return false
	}

	// validate the session token
	return wb.validateSessionToken(cookie.Value)
}

// tryBasicAuth checks if the user is authenticated via basic auth
// and sets a cookie on success
func (wb *Web) tryBasicAuth(w http.ResponseWriter, r *http.Request) bool {
	username, password, ok := r.BasicAuth()

	// if basic auth is not provided or invalid
	if !ok {
		return false
	}

	usernameCorrect := subtle.ConstantTimeCompare([]byte(username), []byte(wb.getAuthUser())) == 1
	passwordCorrect := subtle.ConstantTimeCompare([]byte(password), []byte(wb.Auth)) == 1

	// if credentials don't match
	if !usernameCorrect || !passwordCorrect {
		return false
	}

	// set cookie for future requests
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    wb.generateSessionToken(),
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   wb.getSessionMaxAge(),
	})

	return true
}

// getAuthUser returns the configured auth username or "weblist" as default
func (wb *Web) getAuthUser() string {
	if wb.AuthUser == "" {
		return "weblist"
	}
	return wb.AuthUser
}

// getSessionMaxAge returns the session max age in seconds, defaulting to 24 hours
func (wb *Web) getSessionMaxAge() int {
	maxAge := int(wb.SessionTTL.Seconds())
	if maxAge == 0 {
		return 3600 * 24 // default to 24 hours
	}
	return maxAge
}

// normalizeBrandColor ensures the brand color has a # prefix if it's a hex color
func (wb *Web) normalizeBrandColor(color string) string {
	if color == "" {
		return ""
	}

	// if color doesn't start with #, add it (assuming it's a hex color)
	if !strings.HasPrefix(color, "#") {
		return "#" + color
	}

	return color
}

// generateCSRFToken creates a random token for CSRF protection
func (wb *Web) generateCSRFToken() string {
	const tokenLength = 32
	b := make([]byte, tokenLength)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		// if crypto/rand fails, use uuid which has its own entropy source
		log.Printf("[WARN] Failed to generate random CSRF token: %v, using UUID fallback", err)
		return uuid.NewString()
	}
	return fmt.Sprintf("%x", b)
}

// generateSessionToken creates a secure session token based on a random value
// and the current timestamp, signed with a secret key
func (wb *Web) generateSessionToken() string {
	// create a unique random ID
	tokenID := uuid.NewString()

	// use SessionSecret as the signing key
	secret := []byte(wb.SessionSecret)

	// create HMAC using the secret key
	h := hmac.New(sha256.New, secret)

	// add the token ID to the HMAC
	h.Write([]byte(tokenID))

	// add timestamp to prevent reuse if secret changes and for expiration validation
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	h.Write([]byte(timestamp))

	// get the signature
	signature := h.Sum(nil)

	// combine the token ID, timestamp and signature
	token := tokenID + "." + timestamp + "." + base64.StdEncoding.EncodeToString(signature)
	return token
}

// validateSessionToken validates the session token
func (wb *Web) validateSessionToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	tokenID := parts[0]
	timestamp := parts[1]
	signatureB64 := parts[2]

	// recreate the HMAC signature using the session secret initialized at startup
	secret := []byte(wb.SessionSecret)

	h := hmac.New(sha256.New, secret)
	h.Write([]byte(tokenID))
	h.Write([]byte(timestamp))
	expectedSignature := h.Sum(nil)

	// decode the provided signature
	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}

	// check if signatures match using constant-time comparison
	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return false
	}

	// validate token expiration based on timestamp
	timestampInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	// get session TTL, default to 24 hours if not set
	maxAge := wb.SessionTTL
	if maxAge == 0 {
		maxAge = 24 * time.Hour
	}

	// check if token has expired
	tokenTime := time.Unix(timestampInt, 0)
	return time.Since(tokenTime) <= maxAge
}

// isRequestSecure checks if the request is secure by examining TLS status and common proxy headers
func (wb *Web) isRequestSecure(r *http.Request) bool {
	// if insecure cookies is enabled, we don't care about the request security
	if wb.InsecureCookies {
		return false
	}

	// check if the connection itself is secure
	if r != nil && r.TLS != nil {
		return true
	}

	// check common proxy headers for HTTPS
	if r != nil {
		// x-Forwarded-Proto is the de-facto standard header for proxies
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			return true
		}
		// check Forwarded header (RFC 7239)
		if fwd := r.Header.Get("Forwarded"); fwd != "" {
			// RFC 7239 specifies that Forwarded header may contain multiple
			// comma-separated entries, each with semicolon-separated parameters
			for entry := range strings.SplitSeq(fwd, ",") {
				entry = strings.TrimSpace(entry)
				for part := range strings.SplitSeq(entry, ";") {
					part = strings.TrimSpace(part)
					if strings.HasPrefix(part, "proto=") && strings.ToLower(strings.TrimPrefix(part, "proto=")) == "https" {
						return true
					}
				}
			}
		}
	}

	return false
}
