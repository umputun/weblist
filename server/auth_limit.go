package server

import (
	"net/http"
	"sync"
	"time"

	cache "github.com/go-pkgz/expirable-cache/v3"
)

const (
	maxAuthFailures   = 5                // failed password attempts from one address before it is locked out
	authFailureWindow = 10 * time.Minute // how long failures are remembered, counted from the last one
	maxAuthTracked    = 10000            // addresses tracked at once, bounding memory
)

// authFailures counts failed password attempts per client address. Its zero value is ready to use.
type authFailures struct {
	mu     sync.Mutex
	counts cache.Cache[string, int]
}

func (a *authFailures) blocked(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, _ := a.cache().Get(ip)
	return n >= maxAuthFailures
}

func (a *authFailures) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, _ := a.cache().Get(ip)
	a.cache().Set(ip, n+1, 0)
}

func (a *authFailures) reset(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache().Remove(ip)
}

// cache returns the counter store, creating it on first use; callers hold mu
func (a *authFailures) cache() cache.Cache[string, int] {
	if a.counts == nil {
		a.counts = cache.NewCache[string, int]().WithTTL(authFailureWindow).WithMaxKeys(maxAuthTracked)
	}
	return a.counts
}

// basicAuthLimitMiddleware counts wrong Basic Auth credentials per client address and answers 429 to
// any request carrying credentials once the address is locked out. It runs on every route, public
// ones included, because a public page under PublicRead still reveals whether credentials were right.
// Requests without credentials, cookie sessions among them, pass through untouched.
func (wb *Web) basicAuthLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			next.ServeHTTP(w, r)
			return
		}
		if wb.authFails.blocked(r.RemoteAddr) {
			http.Error(w, "Too many failed login attempts, please try again later", http.StatusTooManyRequests)
			return
		}
		if wb.checkBasicAuth(r) {
			wb.authFails.reset(r.RemoteAddr)
		} else {
			wb.authFails.fail(r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}
