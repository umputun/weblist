package server

import (
	"crypto/subtle"
	"net/http"
	"net/netip"
	"sync"
	"time"

	cache "github.com/go-pkgz/expirable-cache/v3"
)

const (
	maxAuthFailures   = 5                // failed password attempts from one address before it is locked out
	authFailureWindow = 10 * time.Minute // how long failures are remembered, counted from the last one
	maxAuthTracked    = 10000            // addresses tracked at once, bounding memory
)

// AuthLockout counts failed password attempts per client address across the login form, HTTP Basic
// Auth and SFTP. Its zero value is ready to use. Give Web and SFTP the same one, so failures over
// either protocol count together.
type AuthLockout struct {
	mu     sync.Mutex
	counts cache.Cache[string, int]
}

// allow records one password check from addr and reports whether its result may be honored. Once the
// address is locked out it returns false and counts nothing, even for the right password. Otherwise
// a right password clears the count and a wrong one adds to it in the same step, so parallel guesses
// cannot all pass the check before any of them is counted.
func (a *AuthLockout) allow(addr string, valid bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := lockoutKey(addr)
	n := a.count(key)
	if n >= maxAuthFailures {
		return false
	}
	switch {
	case !valid:
		a.cache().Set(key, n+1, 0)
	case n > 0:
		a.cache().Remove(key)
	}
	return true
}

// count returns the live failure count; callers hold mu. The cache hands back an expired entry's
// value with ok false, which must read as zero.
func (a *AuthLockout) count(key string) int {
	if a.counts == nil {
		return 0
	}
	n, ok := a.counts.Get(key)
	if !ok {
		return 0
	}
	return n
}

// cache returns the counter store, creating it on first use; callers hold mu
func (a *AuthLockout) cache() cache.Cache[string, int] {
	if a.counts == nil {
		a.counts = cache.NewCache[string, int]().WithTTL(authFailureWindow).WithMaxKeys(maxAuthTracked)
	}
	return a.counts
}

// lockoutKey groups IPv6 addresses by /64, the block one subscriber usually holds, so rotating through
// it does not reset a count. IPv4 and unparsable values are used as they are.
func lockoutKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() {
		return ip
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// lockout returns the shared Lockout, or this server's own when none was given
func (wb *Web) lockout() *AuthLockout {
	if wb.Lockout != nil {
		return wb.Lockout
	}
	return &wb.ownLockout
}

// basicAuthLimitMiddleware counts wrong Basic Auth passwords per client address and answers 429 to
// any guess once the address is locked out. It runs on every route, public ones included, because a
// public page under PublicRead still reveals whether credentials were right. Only credentials naming
// weblist's user are counted: no other username can authenticate, and a proxy doing its own Basic
// Auth forwards its credentials on every request. A request with a valid session cookie is never
// counted either, as authMiddleware does not look at its credentials.
func (wb *Web) basicAuthLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(wb.getAuthUser())) != 1 || wb.isAuthenticatedByCookie(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !wb.lockout().allow(r.RemoteAddr, wb.checkBasicAuth(r)) {
			http.Error(w, "Too many failed login attempts, please try again later", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
