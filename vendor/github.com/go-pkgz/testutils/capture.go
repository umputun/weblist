package testutils

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
)

// CaptureStdout captures os.Stdout output from the provided function.
func CaptureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = w
	defer func() { os.Stdout = old }()

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		_, err := io.Copy(&buf, r)
		if err != nil {
			t.Errorf("failed to read captured stdout: %v", err)
		}
	}()

	f()
	_ = w.Close()
	wg.Wait()
	return buf.String()
}

// CaptureStderr captures os.Stderr output from the provided function.
func CaptureStderr(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = w
	defer func() { os.Stderr = old }()

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		_, err := io.Copy(&buf, r)
		if err != nil {
			t.Errorf("failed to read captured stderr: %v", err)
		}
	}()

	f()
	_ = w.Close()
	wg.Wait()
	return buf.String()
}

// CaptureStdoutAndStderr captures os.Stdout and os.Stderr output from the provided function.
func CaptureStdoutAndStderr(t *testing.T, f func()) (stdout, stderr string) {
	t.Helper()
	oldout, olderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = oldout
		os.Stderr = olderr
	}()

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, err := io.Copy(&outBuf, rOut)
		if err != nil {
			t.Errorf("failed to read captured stdout: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		_, err := io.Copy(&errBuf, rErr)
		if err != nil {
			t.Errorf("failed to read captured stderr: %v", err)
		}
	}()

	f()
	_ = wOut.Close()
	_ = wErr.Close()
	wg.Wait()

	return outBuf.String(), errBuf.String()
}
