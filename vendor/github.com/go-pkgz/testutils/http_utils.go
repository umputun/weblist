package testutils

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// MockHTTPServer creates a test HTTP server with the given handler.
// Returns the server URL and a function to close it.
func MockHTTPServer(t *testing.T, handler http.Handler) (serverURL string, cleanup func()) {
	t.Helper()

	server := httptest.NewServer(handler)

	cleanup = func() {
		server.Close()
	}

	// register cleanup with t.Cleanup to ensure the server is closed
	// even if the test fails
	t.Cleanup(cleanup)

	return server.URL, cleanup
}

// RequestRecord holds information about a captured HTTP request
type RequestRecord struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// RequestCaptor captures HTTP requests for inspection in tests
type RequestCaptor struct {
	mu       sync.Mutex
	requests []RequestRecord
}

// Len returns the number of captured requests
func (c *RequestCaptor) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// GetRequest returns the request at the specified index
func (c *RequestCaptor) GetRequest(idx int) (RequestRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if idx < 0 || idx >= len(c.requests) {
		return RequestRecord{}, false
	}

	return c.requests[idx], true
}

// GetRequests returns all captured requests
func (c *RequestCaptor) GetRequests() []RequestRecord {
	c.mu.Lock()
	defer c.mu.Unlock()

	// return a copy to avoid race conditions
	result := make([]RequestRecord, len(c.requests))
	copy(result, c.requests)
	return result
}

// Reset clears all captured requests
func (c *RequestCaptor) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = nil
}

// add records a new request
func (c *RequestCaptor) add(rec RequestRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, rec)
}

// HTTPRequestCaptor returns a request captor and HTTP handler that captures requests
// The returned handler will forward requests to the provided next handler if not nil
func HTTPRequestCaptor(t *testing.T, next http.Handler) (*RequestCaptor, http.Handler) {
	t.Helper()

	captor := &RequestCaptor{
		requests: []RequestRecord{},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// create a record from the request
		record := RequestRecord{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
		}

		// read and store the body if present
		if r.Body != nil {
			// read body
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Logf("failed to read request body: %v", err)
			}

			// store the body in the record
			record.Body = bodyBytes

			// replace the body for downstream handlers
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// add the record to the captor
		captor.add(record)

		// forward the request if a next handler is provided
		if next != nil {
			next.ServeHTTP(w, r)
		}
	})

	return captor, handler
}
