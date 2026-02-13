# File Upload Feature

## Overview
- Add optional file upload support to weblist, allowing users to upload files to the currently viewed directory
- Three upload methods: click-to-browse (file picker), drag-and-drop onto file listing, clipboard paste
- Gated behind `--upload.enabled` flag (off by default) with configurable max file size
- Overwrite behavior configurable via `--upload.overwrite` (default: false — returns error if file exists)
- Upload route bypasses global 1MB SizeLimit, uses its own MaxBytesReader

## Context (from discovery)
- files/components involved: `main.go` (options), `server/server.go` (config, routing), `server/handlers.go` (new handler), `server/templates/index.html` (upload button + JS), `server/assets/css/weblist-app.css` (styling)
- related patterns: multi-select feature uses similar toolbar button pattern in `actions-container` div; HTMX partial refresh via `hx-target="#page-content"`
- the upload route needs its own route group without `rest.SizeLimit(1MB)` to allow large uploads
- `fs.FS` is read-only — upload handler must use `os.Create` with `Config.RootDir` path directly
- secrets project has drag-and-drop reference implementation (vanilla JS, no clipboard paste)

## Development Approach
- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility

## Testing Strategy
- **unit tests**: required for every task — handler tests, config validation
- **e2e tests**: project has Playwright-based e2e tests in `e2e/` directory
  - upload UI changes need e2e tests in `e2e/upload_test.go`
  - test upload button visibility, file upload flow, error cases

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## Implementation Steps

### Task 1: Add Upload CLI options and server Config

**Files:**
- Modify: `main.go`
- Modify: `server/server.go`

- [x] add `Upload` options struct to `main.go` with `Enabled bool`, `MaxSize int64` (default 64MB), and `Overwrite bool`
- [x] add `EnableUpload bool`, `UploadMaxSize int64`, and `UploadOverwrite bool` fields to `server.Config`
- [x] wire `Upload.Enabled`, `Upload.MaxSize`, and `Upload.Overwrite` from options to config in `runServer()`
- [x] update existing tests in `main_test.go` if config wiring is tested there
- [x] run tests — must pass before next task

### Task 2: Add upload route with custom size limit

**Files:**
- Modify: `server/server.go`

- [x] remove `rest.SizeLimit(1MB)` from global `router.Use()` chain (it currently applies to ALL routes)
- [x] create upload route group WITHOUT `rest.SizeLimit` — upload handler applies its own `MaxBytesReader`
- [x] create main route group WITH `rest.SizeLimit(1MB)` for all existing routes (preserves current behavior)
- [x] apply auth middleware to upload group when auth is enabled
- [x] only register upload route when `EnableUpload` is true
- [x] write tests in `server/server_test.go` for route registration (upload enabled vs disabled)
- [x] run tests — must pass before next task

### Task 3: Implement upload handler

**Files:**
- Create: `server/upload.go`
- Create: `server/upload_test.go`

- [x] create `handleUpload()` method on `Web` — accepts `multipart/form-data`
- [x] read `path` form field for target directory (clean path, validate no traversal)
- [x] apply `http.MaxBytesReader` with `UploadMaxSize` to request body
- [x] use `r.ParseMultipartForm(10 << 20)` (10MB in-memory buffer, rest spills to temp files)
- [x] check target directory path against `shouldExclude()` — return 403 if excluded
- [x] iterate multipart files, validate filenames (reject `..`, `/`, empty)
- [x] if `UploadOverwrite` is false, check file does not already exist — return 409 Conflict if it does
- [x] if `UploadOverwrite` is true, allow overwriting existing files
- [x] write file to `filepath.Join(RootDir, cleanPath, filename)` using `os.Create`
- [x] check that target directory exists and is within RootDir
- [x] return JSON response with upload status (success/error, filenames)
- [x] write tests for successful single file upload
- [x] write tests for successful multiple file upload
- [x] write tests for path traversal rejection (`../`, absolute paths)
- [x] write tests for duplicate file rejection when overwrite disabled (409)
- [x] write tests for successful overwrite when overwrite enabled
- [x] write tests for oversized file rejection
- [x] write tests for upload disabled (403)
- [x] write tests for excluded path rejection
- [x] run tests — must pass before next task

### Task 4: Add upload button and JavaScript to template

**Files:**
- Modify: `server/templates/index.html`
- Modify: `server/handlers.go` (template data struct in `handleDirContents()`)
- Modify: `server/file_ops.go` (template data struct in `renderFullPage()`)

- [x] add `EnableUpload` and `UploadMaxSize` fields to template data struct in `handleDirContents()` (`server/handlers.go`)
- [x] add `EnableUpload` and `UploadMaxSize` fields to template data struct in `renderFullPage()` (`server/file_ops.go`)
- [x] add upload button in `actions-container` div (next to selection status), conditionally rendered with `{{ if .EnableUpload }}`
- [x] add hidden `<input type="file" multiple>` element
- [x] add JavaScript: click handler on upload button triggers hidden file input
- [x] add JavaScript: `change` event on file input calls `uploadFiles()` function
- [x] add JavaScript: `dragover`/`dragleave`/`drop` events on `<article>` (file listing area)
- [x] add JavaScript: `paste` event on `document` to handle clipboard file paste
- [x] implement `uploadFiles(files, path)` — creates FormData, sends `fetch("POST /upload")`, refreshes file listing via HTMX on success
- [x] add visual feedback: drag-over highlight class on file listing, toast notification for success/error
- [x] add file size validation in JS before upload (show error if exceeds max)
- [x] run tests — must pass before next task

### Task 5: Add upload styling

**Files:**
- Modify: `server/assets/css/weblist-app.css`

- [x] style upload button consistent with existing toolbar controls
- [x] add `.drag-over` highlight style for file listing area during drag
- [x] add upload toast/notification styling (reuse `.htmx-error-container` pattern)
- [x] ensure upload button works in both light and dark themes
- [x] visually verify both themes render correctly

### Task 6: Verify acceptance criteria

Use `agent-browser` skill to automate browser-based verification against a running local server.

- [x] start local server with upload enabled: `go run . --dbg --root=/tmp/weblist-test --upload.enabled --upload.max-size=1`
- [x] start second server without upload for disabled check: `go run . --dbg --root=/tmp/weblist-test --listen=:8081`
- [x] use agent-browser to verify upload button is visible on :8080
- [x] use agent-browser to verify upload button is NOT visible on :8081 (upload disabled)
- [x] use agent-browser to upload a file via file picker and verify it appears in listing
- [x] use agent-browser to upload a duplicate file and verify error when overwrite disabled
- [x] use agent-browser to verify oversized file is rejected (max-size=1MB)
- [x] verify path traversal is blocked via curl: `curl -F "path=../../etc" -F "file=@test.txt" localhost:8080/upload`
- [x] run full unit test suite: `go test ./...`
- [x] run linter: `golangci-lint run ./...`
- [x] verify test coverage meets 80%+
- [x] stop test servers and clean up `/tmp/weblist-test`

### Task 7: Add e2e tests for upload

**Files:**
- Create: `e2e/upload_test.go`

- [x] add e2e test for upload button visibility (enabled vs disabled)
- [x] add e2e test for file upload via file picker
- [x] add e2e test for duplicate file rejection in UI
- [x] add e2e test for upload with auth enabled
- [x] run e2e tests: `make e2e`

### Task 8: [Final] Update documentation

- [x] update README.md with upload feature documentation (CLI flags, usage)
- [x] update CLAUDE.md if new patterns discovered
- [x] move this plan to `docs/plans/completed/`

## Technical Details

**CLI options:**
```
Upload options:
  --upload.enabled    enable file upload (env: UPLOAD_ENABLED)
  --upload.max-size   max upload size in MB (default: 64) (env: UPLOAD_MAX_SIZE)
  --upload.overwrite  allow overwriting existing files (env: UPLOAD_OVERWRITE)
```

**Upload endpoint:**
- `POST /upload` with `multipart/form-data`
- Form fields: `path` (target directory), `file` (one or more files)
- Response: JSON `{"uploaded": ["file1.txt", "file2.txt"]}` or `{"error": "message"}`

**Route structure change:**
```
router (global middleware: trace, realIP, recovery, throttle, rate limit, logger, appInfo, security headers)
├── upload group (auth middleware, NO SizeLimit) → POST /upload with MaxBytesReader
├── main group (auth middleware, SizeLimit 1MB) → all existing routes
├── login/logout routes
└── assets route
```

**Server timeout note:**
- Current `ReadTimeout` is 10s which may be insufficient for large uploads over slow connections
- When upload is enabled, increase `ReadTimeout` to accommodate `UploadMaxSize` (e.g., 5 min)
- Alternative: document as known limitation and let users adjust if needed

**Security considerations:**
- path traversal protection via `filepath.Clean` + verify result is within RootDir
- filename sanitization: reject `..`, `/`, `\`, empty names
- `http.MaxBytesReader` enforces server-side size limit
- overwrite disabled by default to prevent accidental data loss (configurable via `--upload.overwrite`)
- auth middleware protects upload when auth is enabled

## Post-Completion

**Manual verification:**
- test upload with various file types and sizes
- test drag-and-drop from file manager
- test clipboard paste with screenshot/image
- verify upload button appearance in both themes on different screen sizes
