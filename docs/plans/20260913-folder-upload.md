# Folder Upload

## Overview
- Let users upload a whole folder tree: drag/drop a directory onto the listing, or pick one with a new "Folder" toolbar button. Files land under the current directory with their relative subdirectories recreated.
- The client walks the dropped tree and sends one `POST /upload` per file. The endpoint keeps its current multipart shape; the only server-side change for folders is that the `path` field may name a subdirectory that does not exist yet, which the server creates under a validated existing ancestor after every part of the request has passed validation.
- Two inherited gaps are fixed in the same change because the folder feature would otherwise promise things the server does not enforce:
  - excludes are checked on the target directory only, so `--exclude .env` still lets a file named `.env` be written;
  - the size limit bounds the whole request body while the client checks each file, so a file near the limit passes the client and fails the server.
- Gated by the existing `--upload.enabled`. No new flag. Enabling upload now also permits directory creation, which the README states explicitly.
- Out of scope: empty directories inside a dropped folder are not recreated. The file-count cap per selection is a constant in the client, not a flag.
- Not in this plan: select-all not marking rows in a subdirectory and filename clicks landing on the checkbox. Both reproduce nothing on master; they are issues #53 and #54, fixed by PR #55 in v0.20.4. The instance that showed them runs v0.20.1 and needs an upgrade.

## Context (from discovery)
- files/components involved: `server/upload.go` (handler, path and filename validation, file write), `server/upload_test.go`, `server/file_ops.go` (`shouldExclude` / `matchesExcludes`), `server/server.go` (router, global tollbooth limiter), `server/templates/index.html` (upload button and the upload IIFE inside the HTMX-swapped `#page-content`), `server/assets/css/weblist-app.css` (`.upload-button`, `.drag-over`, `.upload-toast`), `e2e/upload_test.go`, `README.md`, `CLAUDE.md`
- related patterns found:
  - errors carry an HTTP status through `*uploadError{status, msg}`, unwrapped with `errors.AsType` in the handler and rendered by `wb.writeJSONError`
  - `validateUploadPath` order: clean, reject absolute, reject `..`, `shouldExclude`, `os.Stat`, `EvalSymlinks` on target and root, prefix compare. `os.Stat` follows symlinks, so a symlinked directory inside the root is a valid target today and must stay one.
  - `matchesExcludes` matches a pattern wherever its components appear as a contiguous run, so a directory pattern already covers everything beneath it
  - `handleUpload` has cyclomatic complexity 15 against a gocyclo threshold of 15 (reports at 16+); any added branch fails the lint gate unless the part loop is extracted first
  - `validateUploadPath(path string)` shadows the `path` package name; gocritic `importShadow` is enabled, so joins use `filepath.Join`
  - the global limiter is `tollbooth.NewLimiter(50, nil)` at `router.Use`, 50 rps with burst 50, keyed by client IP plus URL path (`BuildKeys` appends `r.URL.Path`; `IgnoreURL` defaults to false), so `/upload` has its own bucket per client and navigation, assets and the listing refresh are limited separately; its 429 body is `text/plain`
  - the upload script is rendered inside `#page-content`, so every HTMX navigation re-runs it with a fresh closure; the paste handler already works around this through `window._weblistPasteHandler`
  - the post-upload refresh uses the `currentPath` captured at render, so a refresh finishing after navigation swaps the old directory back in
  - the vendored htmx is 1.9.10 (CLAUDE.md says v2; the docs task corrects that). It emits `htmx:afterSwap` on normal swaps and `htmx:historyRestore` when Back/Forward restores `#page-content` from the history cache, passes `headers` from the `htmx.ajax` context object, and exposes `requestConfig.headers` in the `htmx:beforeSwap` detail
  - `securityHeadersMiddleware` sets `script-src 'self' 'unsafe-inline' 'unsafe-eval'`, so a static script under `/assets/js/` needs no CSP change
  - unit tests are plain `Test...` functions with `t.TempDir()` and the `createMultipartRequest` helper; e2e tests start the real binary per port via `startUploadServer`, which copies all of `e2e/testdata` into the served root, and drive Playwright. Ports 18080 to 18084 are taken.
  - the vendored playwright-go (v0.6201.1) has `Page.Route`, `Route.Fetch`, `Route.Fulfill` with `RouteFulfillOptions.Response`, and `SetInputFiles` accepts a single directory path
- dependencies identified: none new. `webkitGetAsEntry` and `webkitdirectory` are non-standard but supported by Chrome, Firefox and Safari. `webkitGetAsEntry` returns null outside the drop callback's drag-data-store mode, so top-level entries are captured synchronously in the drop handler.

## Development Approach
- **testing approach**: TDD (tests first)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility: the batch form (several `file` parts in one request) keeps working under the aggregate body ceiling; only the per-file size check and the full-path exclude check are new rejections

## Code-Quality Rules (HARD — verify against every task before marking complete)

These rules supplement project CLAUDE.md and are NOT optional. They are the gate for marking any task complete. If a rule is violated, the task is not done — refactor, re-test, then mark complete.

**Signatures (hard limits):**
- No function or method has 4+ parameters. `ctx context.Context` does not count toward the budget. If you need 4+, use an option struct (e.g., `type fooOpts struct { ... }`).
- No function or method has 4+ return values. Split the function into two single-purpose ones, or return a struct.
- Multiple adjacent same-type parameters (`oldLine, newLine int`) are a swap hazard — review whether they belong on a struct.

**Methods vs standalone helpers (project rule, hard):**
- If a function is called only from methods of a single struct, it MUST be a method on that struct. Calling pattern decides, not field access.
- Standalone helpers are reserved for: (a) constructors and entry points (`Parse...`, `New...`, `Decorate...`), (b) utilities shared by multiple unrelated types or by both standalone functions AND methods, (c) tiny cross-cutting helpers.
- Before adding any standalone helper, mentally walk its callers. If every caller is a method of one type, make the helper a method on that type.

**Visibility (private by default, hard):**
- Lowercase identifiers by default. Only export when an out-of-package caller exists.
- Exception (per CLAUDE.md): methods called by other structs in the same package CAN be exported for inter-component API clarity. This is the only exception. It does not extend to types, functions, constants, or variables.
- Before exporting any new identifier, grep for cross-package callers. If none, lowercase it.

**Comments (default: none, hard):**
- Default to writing no comments. Add one only when the WHY is non-obvious (a hidden invariant, a workaround, behavior that would surprise a reader).
- Exported items get godoc comments starting with the name. Unexported items get lowercase non-godoc comments — or no comment at all.
- Never describe WHAT the code does when the code itself is self-evident. Never write multi-paragraph comments on routine helpers.

**Per-task gate (before marking ANY checkbox complete):**
1. Formatter runs clean (`~/.claude/format.sh` or `gofmt -s -w` + `goimports -w`).
2. `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` reports zero issues.
3. `go test ./... -race` passes.
4. Scan the new code for the four rule classes above. Specifically:
   - Grep new function signatures: `grep -nE '^func.*\(.*,.*,.*,.*\)' app/<path>/*.go` — any hit with 4+ comma-separated params (excluding `ctx`) is a violation. Same for the return-value side.
   - For every new standalone helper, `grep -rn 'helperName(' --include='*.go'` and confirm at least one caller is NOT a method of a single type. If all callers are methods of one type, convert.
   - For every new exported identifier, grep cross-package. If no out-of-package hit, lowercase it.
5. Only after 1–4 pass: mark the task complete.

If a previous task shipped a violation (spotted later by user, reviewer, or yourself): fix it in the next commit BEFORE starting the next task. Do not let violations accumulate.

Project-specific notes for this plan:
- the project's own lint command is `golangci-lint run ./...`; the gate above runs it from the repo root
- the JavaScript file has no linter in this repo; keep it in the same plain ES5 style as the current inline script

## Testing Strategy
- **unit tests**: required for every task. Server tests stay in `server/upload_test.go` (one test file per source file), using `t.TempDir()` and `createMultipartRequest`.
- **e2e tests**: Playwright-based tests in `e2e/` with the `e2e` build tag. UI tasks add tests to `e2e/upload_test.go`. The folder path is exercised through the `webkitdirectory` input with `SetInputFiles` on a directory generated in a separate `t.TempDir()`, never under `e2e/testdata`, because `startUploadServer` copies that directory into the served root and the same files would then collide with 409. Timing-dependent behavior (navigation during upload, 429 retry, duplicates against queued entries) is made deterministic with `Page.Route`: intercept `/upload` or the refresh `GET`, hold the request behind a test-controlled channel, act, then `Route.Continue`. Holding the request rather than the response is equivalent for the client (its `fetch` is pending either way) and is the only correct choice for uploads: `Route.Fetch` re-sends the request from Playwright's own stack and Chromium does not expose file blob bytes to it, so every held upload arrived with an empty body (measured in Task 3). Run with `make e2e`.
- the client has no unit-test harness. The directory walk is the one piece of client logic with branches a picker cannot reach (readEntries pagination, early cutoff, null items, reader errors), so the walk takes its entry list as a parameter and one Playwright test drives it with a fake entry tree built in `page.Evaluate`. That test is labelled as algorithm coverage; a real directory drop stays a manual check.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview
- **One request per file.** The client turns a drop, a file pick, a folder pick or a paste into a flat list of `{file, relativeDir}` entries and pushes each through one queue that sends `POST /upload` with `path = targetDir + "/" + relativeDir`. Reasons over a manifest batch: the endpoint shape does not change; each file gets its own outcome instead of the batch aborting on the first bad file with earlier files already written; and the size limit means the same thing on both sides.
- **Selections, not one batch.** Every drop or pick creates a selection object `{targetDir, total, done, failed, skipped}` and each queued entry keeps an immutable reference to it plus its own destination. `targetDir` is captured synchronously when the drop or pick starts, before any asynchronous walk, so navigating during discovery cannot redirect files. Workers, reservations and the backoff deadline are global. A selection's outcome is reported when its last entry settles; preflight refusals and duplicate skips count toward the selection even when nothing is queued.
- **The queue lives outside the swapped template.** The script moves to `server/assets/js/upload.js`, loaded from `<head>`, and keeps its state on a single module object created once per page load. The template renders `#upload-controls` with `data-max-size` and `data-path`; `init()` reads them and rebinds element listeners on `DOMContentLoaded`, on `htmx:afterSwap` of `#page-content`, and on `htmx:historyRestore`. The document-level paste listener binds once at script load. HTMX navigation therefore changes the target for new selections without dropping in-flight or queued uploads.
- **Rate limiter aware.** The global limiter is 50 rps, burst 50, per client IP and URL path, and returns a plain-text 429 before the handler runs. Request starts are spaced at least 25 ms apart across all workers (at most 40 rps) to keep the `/upload` bucket from tripping in the common case; navigation and the listing refresh sit in their own buckets and gain nothing from the pacing. The scheduler updates `lastSendAt` immediately before every `fetch`, retries included, and rechecks it after any wait so several workers waking together cannot send at once. A 429 on an upload is still retried: a shared `backoffUntil` deadline is set that every send respects, a later 429 only extends it, and the waiting entry keeps its reservation and its worker slot; attempts are bounded. Any other failure, including a network error, is final because the write may have happened.
- **Success means the upload JSON.** A response counts as uploaded only when it is `200` with a JSON body carrying `uploaded`. The auth middleware can answer an expired session with an HTML login page that `fetch` sees as `200`, so status alone is not enough; that case is reported as a failure naming the file.
- **Destination reservations.** A set of destination paths covering queued and active entries refuses a duplicate: within one selection the duplicate is skipped and reported, and a new selection overlapping in-flight or queued work has the overlapping entries skipped and reported. This is a per-page guard, not a server guarantee; two tabs can still race, which is the existing overwrite behavior.
- **Bounded input.** `maxFiles` is a constant of 1000. A picker's `FileList` is refused whole when it is longer. A drop walk stops as soon as it has collected `maxFiles + 1` entries, discards the selection and reports "more than 1000 files; the limit is 1000" without claiming to know the total. `webkitGetAsEntry` returning null and reader errors settle the walk with a visible outcome instead of a silent stop.
- **One refresh at the end, guarded twice.** When the global queue drains, the listing refreshes once if the live `data-path` equals the `targetDir` of any selection that finished since the last refresh. The refresh request carries a marker header, and a `htmx:beforeSwap` listener drops the swap when the live path no longer matches, which covers a navigation that completes between the refresh being sent and its response arriving. A summary toast per selection reports counts and lists failures and skips by relative path, since a tree often repeats a basename.
- **Server validates everything, then creates the directory.** `validateUploadPath` walks up from the requested path to the deepest existing ancestor with `os.Stat` (following symlinks, as today), resolves it with `EvalSymlinks`, checks containment under the resolved root, and validates every missing component with `validateFilename`; `shouldExclude` still runs on the full relative directory first, before touching the filesystem. The parts are then validated (filename, full-path exclude, size). Only when every check has passed does `ensureUploadDir` create the directory, immediately before the first write, so a validation-rejected request never leaves an empty directory behind. A write failure after that point, such as a full disk, can still leave the directory; rollback is not part of this plan. The check-then-create gap between `EvalSymlinks` and `MkdirAll` is the same class of gap the current code has between `EvalSymlinks` and `OpenFile`; it needs local filesystem write access to exploit and is accepted rather than closed.
- **Excludes cover the file name.** Every part is checked with `shouldExclude` on `cleanPath/filename`. A dropped folder whose name matches an exclude pattern is refused at the directory level before any file is written; a nested match (for example a `.git` deeper inside the tree) fails only the entries under it, and the summary lists them.
- **Per-file size.** The body is bounded at `UploadMaxSize + uploadOverheadBytes` (8 KB, covering the multipart preamble and the `path` field) and each part's `Size` is compared with `UploadMaxSize`, so a file exactly at the limit is accepted and one byte over is refused with 413. The aggregate ceiling still applies to legacy multi-part requests; per-file enforcement does not lift it. The in-memory multipart buffer drops from 10 MB to 1 MB because one file per request times several in flight would otherwise hold tens of megabytes.

## Technical Details

### Server (`server/upload.go`)

```go
// body bound above UploadMaxSize that covers the multipart preamble and the path field
const uploadOverheadBytes = 8 << 10

func (wb *Web) validateUploadPath(path string) (string, error)

// os.Stat rather than Lstat: a symlinked directory inside the root is a valid target today and stays one
func (wb *Web) existingAncestor(cleanPath string) (ancestor string, missing []string, err error)

func (wb *Web) validateParts(cleanPath string, files []*multipart.FileHeader) error

// an existing directory is success: concurrent per-file requests into one new folder must not fail each other
func (wb *Web) ensureUploadDir(cleanPath string) error

func (wb *Web) storeUploadedFiles(cleanPath string, files []*multipart.FileHeader) ([]string, error)

// writeUploadError maps an *uploadError to its status, anything else to 500 with fallback as the message
func (wb *Web) writeUploadError(w http.ResponseWriter, err error, fallback string)
```

`existingAncestor` returns the deepest existing ancestor of `cleanPath` and the components still to be created; a non-directory ancestor is an `*uploadError` with status 400. `validateParts` checks every part before anything is written: filename, exclude on the full destination path, content size. `ensureUploadDir` creates the directory with mode `0o750`. `storeUploadedFiles` writes the validated parts and returns the stored names. The comments shown above are the only ones the new code carries.

Handler flow after the change:
1. `MaxBytesReader(w, r.Body, wb.UploadMaxSize+uploadOverheadBytes)`; `ParseMultipartForm(1 << 20)`; a `*http.MaxBytesError` still maps to 413 "file too large"
2. `validateUploadPath` returns `cleanPath`, tolerant of missing trailing components
3. reject an empty `file` list with 400 as now
4. `validateParts(cleanPath, files)`: `validateFilename` → 400; `shouldExclude(filepath.Join(cleanPath, fh.Filename))` → 403 "access denied"; `fh.Size > wb.UploadMaxSize` → 413 naming the file
5. `ensureUploadDir(cleanPath)`
6. `storeUploadedFiles(cleanPath, files)` with the current open, `writeUploadedFile`, close and log per part

Every error from steps 2, 4, 5 and 6 goes through `writeUploadError(w, err, fallback)`, which unwraps `*uploadError` once. Measured: the handler is at complexity 15 today against a threshold of 15; extracting `storeUploadedFiles` alone leaves Task 2's end state at 16, and collapsing the four error sites onto `writeUploadError` brings it to 12. Both extractions happen in Task 1 before any new branch. `validateUploadPath` calls `existingAncestor` after the `shouldExclude(cleanPath)` check; it then runs `EvalSymlinks` on the ancestor and the root and the prefix compare as now, and `validateFilename` on each missing component (failure → 400 "invalid directory name"). Joins use `filepath.Join`; `matchesExcludes` normalizes to slashes itself.

Response shape is unchanged: `{"uploaded": [...]}` on success, `{"error": "..."}` otherwise, one status per request.

### Client (`server/assets/js/upload.js`)

Module state created once per page load on `window.weblistUpload`: `queue` (array of `{file, relativeDir, dest, selection, attempts}`), `active` (count), `reserved` (object keyed by `dest`), `selections` (array of `{targetDir, total, done, failed: [], skipped: [], refreshPending}`), `backoffUntil` and `lastSendAt` (timestamps shared by all workers). The same object exposes `walkEntries` and `enqueue`; that is the test seam the fake-entry-tree tests call through `page.Evaluate`, and nothing else in the page uses it.

Toast strings, fixed here and asserted verbatim by the tests: "Uploaded 1 file" and "Uploaded N files" for N other than 1; ", M failed: a/b.txt (file "b.txt" already exists), c.txt (network error)" where each failure carries the server's `error` text, the status text, or the client reason in parentheses; ", K skipped as duplicates". A selection with nothing uploaded starts with "Uploaded 0 files".

Constants: `maxInFlight = 4`, `minSendGapMs = 25`, `maxFiles = 1000`, `max429Retries = 5`, backoff `250ms * 2^attempt` capped at 4 s.

Entry points, each creating a selection with `targetDir` read from `#upload-controls` `data-path` at the moment of the event, then funnelling into `enqueue(selection, entries)`:
- file input `change`: entries from `input.files` with `relativeDir = ""`; refuse the whole `FileList` when longer than `maxFiles`
- folder input (`webkitdirectory`) `change`: `relativeDir` from `file.webkitRelativePath` minus the file name; same `FileList` cap
- drop: capture `dataTransfer.items[i].webkitGetAsEntry()` for every item synchronously in the handler; null results are passed to `enqueue` as discovery errors named by item index; then `walkEntries(entries, budget)` asynchronously. If no item yields an entry, fall back to `dataTransfer.files` as today.
- paste: unchanged renaming, `relativeDir = ""`, listener bound once at script load

`walkEntries(entries, budget)`: returns a promise of `{entries: [...], truncated: bool, errors: [{relativePath, reason}]}`. A file entry yields one `{file, relativeDir}`; a directory entry is read with `createReader().readEntries` in a loop until an empty batch (readers return at most 100 entries per call in Chrome), recursing with the accumulated `relativeDir`. Every file entry encountered counts toward the budget whether or not its `file()` call succeeds, and the walk stops with `truncated` set as soon as `budget + 1` file entries have been seen, so a tree of unreadable files cannot walk past the cap. A `file()` failure records `{relativePath, reason}` and continues; a reader failure records the directory's relative path and skips that directory. The function takes its entries as a parameter so a test can pass a fake tree.

`enqueue(selection, entries, errors)`: refuse the whole selection with a toast when `truncated` or when the collected count exceeds `maxFiles`; otherwise `total` is the collected entries plus the discovery errors, the discovery errors go straight into `failed` by relative path, and null drop items are reported the same way; refuse any entry whose `file.size > maxSize` with a failure naming its relative path; for each remaining entry compute `dest` by joining `targetDir`, `relativeDir` and `file.name` with `/`, dropping empty segments and a leading `.` (so at the root `dest` is `relativeDir/name` or just `name`), skip and record entries already in `reserved`, reserve the rest, push in order, then start workers up to `maxInFlight`. The queue is FIFO: workers take entries in the order they were pushed, which is what the queued-duplicate test relies on to know which two of six sit waiting. If nothing was queued, including a selection where every entry was unreadable, settle the selection immediately and show its summary.

Worker loop: wait until both `backoffUntil` and `lastSendAt + minSendGapMs` have passed, pop an entry, build `FormData` with `path` and one `file`, `fetch('/upload')`. On 429 with attempts remaining, extend `backoffUntil` (never shorten it), keep the reservation and the slot, wait, and retry the same entry; any other status is final. Success requires status 200 and a JSON body with `uploaded`; otherwise read JSON `error` when the content type is JSON, else use the status text, and record the failure as `{relativePath, reason}`. A network error is recorded as final with reason "network error". Release the reservation when the entry settles either way, update its selection, and when the selection's `done + failed + skipped` reaches `total` show its summary toast in the fixed format above and mark `refreshPending`.

Refresh: when `active` is 0 and the queue is empty, collect selections with `refreshPending`; if any has `targetDir` equal to the live `data-path`, send one `htmx.ajax('GET', '/partials/dir-contents?path=...', {target: '#page-content', swap: 'innerHTML', headers: {'X-Upload-Refresh': targetDir}})` and clear `refreshPending` on all of them. A `htmx:beforeSwap` listener on `document.body` sets `shouldSwap = false` when the request carried `X-Upload-Refresh` and its value no longer equals the live `data-path`.

Init: `init()` reads the `data-` attributes from `#upload-controls` and binds the two buttons, the two inputs and the drop zone, replacing element listeners each time; it runs on `DOMContentLoaded`, on `htmx:afterSwap` whose target is `#page-content`, and on `htmx:historyRestore`. It never recreates the module state. The template's inline script block is removed.

### Template (`server/templates/index.html`)

End state after Task 5. Task 3 lands only the `#upload-controls` container with its `data-` attributes, the existing Upload button and the existing file input; the Folder button and the `webkitdirectory` input are added in Task 5. Both buttons are styled by the existing `.upload-button button` rule, so no CSS change is planned.

```html
<div id="upload-controls" class="upload-button" data-max-size="{{ .UploadMaxSize }}" data-path="{{ .Path }}">
    <button type="button" id="upload-btn" title="Upload files">...Upload</button>
    <button type="button" id="upload-folder-btn" title="Upload folder">...Folder</button>
    <input type="file" id="upload-file-input" multiple style="display:none">
    <input type="file" id="upload-folder-input" webkitdirectory style="display:none">
</div>
```

`<script src="/assets/js/upload.js"></script>` goes in the `<head>` next to htmx, inside `{{ if .EnableUpload }}`.

### Rate limiter
No server change. Uploads are paced under the limit of their own bucket and 429 is retried on uploads only; navigation and the refresh are limited in separate buckets and need no handling here. Tests inject 429 through `Page.Route` rather than trying to exhaust the real bucket.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates
- **Post-Completion** (no checkboxes): items requiring external action - manual testing, changes in consuming projects, deployment configs, third-party verifications

## Implementation Steps

### Task 1: Enforce the size limit per file and excludes on the full destination path

**Files:**
- Modify: `server/upload.go`
- Modify: `server/upload_test.go`

- [x] extract the part loop of `handleUpload` into `storeUploadedFiles` and the four error sites onto `writeUploadError`, no behavior change; existing tests stay green and gocyclo reports the handler at or under 12
- [x] write failing test `TestHandleUpload_FileExactlyAtMaxSize` (content of `UploadMaxSize` bytes → 200)
- [x] write failing test `TestHandleUpload_FileOneByteOverMaxSize` (413, error names the file, nothing written)
- [x] write regression guard `TestHandleUpload_BodyOverAggregateLimit` (several parts whose total exceeds `UploadMaxSize + uploadOverheadBytes` → 413 "file too large"; this one passes against the current code and pins the aggregate ceiling)
- [x] write failing test `TestHandleUpload_ExcludedFilename` (`Exclude: [".env"]`, upload `.env` to `.` → 403, nothing written)
- [x] write failing test `TestHandleUpload_ExcludedNestedFilename` (`Exclude: ["secrets/key"]`, upload `key` to `secrets` → 403)
- [x] write failing test `TestHandleUpload_PartsValidatedBeforeAnyWrite` (two parts, second has an excluded name → 403 and the first is not written)
- [x] add `uploadOverheadBytes`, bound the body at `UploadMaxSize + uploadOverheadBytes`, drop the multipart buffer to `1 << 20`
- [x] add `validateParts` with the filename, full-path exclude and size checks and call it before `storeUploadedFiles`
- [x] keep `TestHandleUpload_MultipleFiles` and `TestHandleUpload_OversizedFile` passing; adjust the oversized fixture only if it relied on the exact whole-body bound
- [x] run tests - must pass before next task

### Task 2: Accept and create not-yet-existing subdirectories in the upload path

**Files:**
- Modify: `server/upload.go`
- Modify: `server/upload_test.go`

- [x] write failing test `TestHandleUpload_CreatesMissingSubdirectory` (`path=new/deep`, file written at `new/deep/f.txt`, directories created with mode `0o750`)
- [x] write failing test `TestHandleUpload_MissingSubdirectoryUnderFile` (`path=file.txt/sub` where `file.txt` is a file → 400 "target path is not a directory")
- [x] write failing test `TestHandleUpload_MissingSubdirectoryExcluded` (`Exclude: [".git"]`, `path=proj/.git/objects` → 403, nothing created)
- [x] write failing test `TestHandleUpload_MissingSubdirectoryInvalidComponent` (a component with a backslash → 400, nothing created)
- [x] write failing test `TestHandleUpload_MissingSubdirectoryUnderInRootSymlink` (ancestor is a symlink to a directory inside root → 200, file written through the link)
- [x] write failing test `TestHandleUpload_MissingSubdirectoryUnderSymlinkOutsideRoot` (ancestor is a symlink to a directory outside root → 400, nothing created)
- [x] write failing test `TestHandleUpload_MissingSubdirectoryNotCreatedOnRejectedPart` (`path=new/deep` with an excluded filename → 403 and `new` does not exist afterwards)
- [x] write failing test `TestHandleUpload_ConcurrentCreateSameSubdirectory` (two requests into the same new dir both succeed)
- [x] add cases to `TestValidateUploadPath` for missing components and rewrite `TestHandleUpload_NonexistentDirectory` to the new behavior
- [x] add `existingAncestor` and change `validateUploadPath` to use it, keeping `shouldExclude(cleanPath)` before the walk and the symlink containment check on the ancestor
- [x] add `ensureUploadDir` and call it from `handleUpload` after `validateParts`, before `storeUploadedFiles`
- [x] run tests - must pass before next task

### Task 3: Move the upload script to a static file with a per-file queue

**Files:**
- Create: `server/assets/js/upload.js`
- Modify: `server/templates/index.html`
- Modify: `e2e/upload_test.go`

- [x] update `TestUpload_ToastOnSuccessfulUpload` to assert "Uploaded 1 file" and `TestUpload_ToastOnDuplicateFile` to assert the failure entry "sample.txt (file "sample.txt" already exists)" in the summary
- [x] ➕ write failing e2e `TestUpload_FilenamesMatchingObjectPropertiesAreUploaded` (files named `constructor`, `toString`, `__proto__` reach disk; pins the reservation map being null-prototype, found in codex's Task 3 review)
- [x] write failing e2e `TestUpload_NetworkErrorNotRetried` (route aborts; summary lists the file as failed with "network error"; exactly one attempt)
- [x] write failing e2e `TestUpload_DuplicateAgainstQueuedEntrySkipped` (hold every upload response with `Route.Fetch`; pick six files and wait until four requests are intercepted so two sit queued; pick one of the two queued names again; release all; the second selection reports 1 skipped and the first uploads all six)
- [x] write failing e2e `TestUpload_OverlappingSelectionsKeepTheirTargets` (hold the responses of a selection started in the root; navigate into a subdirectory and pick a different file there; release; each file lands in the directory captured when its selection started, and each selection shows its own summary)
- [x] write failing e2e `TestUpload_SummaryToastListsFailures` (one oversize file among valid ones; summary names it as failed with the size reason, others uploaded)
- [x] write failing e2e `TestUpload_HtmlLoginResponseReportedAsFailure` (route fulfils `/upload` with a `200` HTML body; summary lists the file as failed)
- [x] create `upload.js` with the module state on `window.weblistUpload`, selections, `enqueue`, workers with `maxInFlight = 4`, reservations, the JSON success check, the summary toast, a single end-of-queue refresh guarded by the live path, and `init()` bound to load and `htmx:afterSwap`; no pacing, no 429 handling, no `beforeSwap` guard and no history binding yet
- [x] replace the inline `<script>` block in `index.html` with `#upload-controls` `data-` attributes and a `<script src="/assets/js/upload.js">` in the head under `{{ if .EnableUpload }}`
- [x] keep the existing e2e tests passing: single file, duplicate rejected at the API, path traversal, auth, disabled server
- [x] run unit and e2e tests - must pass before next task

### Task 4: Pacing, 429 retry, refresh guard and history rebinding

**Files:**
- Modify: `server/assets/js/upload.js`
- Modify: `e2e/upload_test.go`

- [ ] write failing e2e `TestUpload_ManyFilesComplete` (60 small files through the file input; all appear in the listing; summary reads "Uploaded 60 files"; moved here from Task 3 because 60 unpaced requests exhaust the limiter's burst of 50, measured: 51 uploaded and 9 refused with 429)
- [ ] write failing e2e `TestUpload_RetriesOn429` (`Page.Route` on `/upload` answers the first two attempts with a plain-text 429, then passes through; file uploaded; attempt count is 3)
- [ ] write failing e2e `TestUpload_StopsAfterRetryLimit` (route always answers 429; summary lists the file as failed after `max429Retries + 1` attempts)
- [ ] write failing e2e `TestUpload_NavigationDuringUploadKeepsNewDirectory` (hold the upload response; click the real HTMX link into a subdirectory; wait for its listing; release; wait for completion; assert the subdirectory listing and URL remain)
- [ ] write failing e2e `TestUpload_LateRefreshDoesNotReplaceNavigation` (let the upload finish; route `**/partials/dir-contents*` and `Continue` every request without the `X-Upload-Refresh` header so navigation is not held, hold only the one carrying it; navigate into a subdirectory; release; assert the subdirectory listing remains)
- [ ] write failing e2e `TestUpload_ControlsWorkAfterHistoryBack` (navigate into a subdirectory and Back; upload through the file input; file lands in the root listing)
- [ ] add `minSendGapMs` pacing with `lastSendAt` updated immediately before every `fetch` and rechecked after waits, the shared 429 backoff, the `X-Upload-Refresh` marker with the `htmx:beforeSwap` guard, and `init()` on `htmx:historyRestore`
- [ ] run unit and e2e tests - must pass before next task

### Task 5: Folder drop walk and folder picker button

**Files:**
- Modify: `server/assets/js/upload.js`
- Modify: `server/templates/index.html`
- Modify: `e2e/upload_test.go`

- [ ] write failing e2e `TestUpload_FolderPickerCreatesTree` (generate a two-level tree in a separate `t.TempDir()`; `SetInputFiles` with that directory on `#upload-folder-input`; nested files appear at their relative paths under the served root)
- [ ] add `uploadExcludeURL = "http://localhost:18085"` next to the existing port constants; it is the only new server this plan starts
- [ ] write failing e2e `TestUpload_FolderPickerHonorsExcludeOnFilename` (server started with `--exclude .env` on port 18085; the tree's `.env` is reported failed by relative path with the server's "access denied" text, the rest uploaded)
- [ ] write failing e2e `TestUpload_FolderPickerRefusesOverMaxFiles` (generated tree of 1001 tiny files; toast names the limit; no `/upload` request is made)
- [ ] write failing e2e `TestUpload_WalkEntriesAlgorithm` (fake entry tree built in `page.Evaluate` and passed to `window.weblistUpload.walkEntries`, covering `readEntries` pagination, cutoff at `budget + 1` with `truncated` set, a reader error and a `file()` failure recorded by relative path, and a null item; assert the selection's final summary and completion, not only the walk result; labelled as algorithm coverage)
- [ ] write failing e2e `TestUpload_AllUnreadableSelectionSettles` (fake tree where every `file()` fails, driven through `window.weblistUpload.enqueue`; no `/upload` request; summary lists every path as failed)
- [ ] write failing e2e `TestUpload_FolderButtonVisibleWhenEnabled` and hidden when disabled
- [ ] add `walkEntries` with the `readEntries` loop, the early cutoff and error counting, and the drop handler's synchronous entry capture with the `dataTransfer.files` fallback
- [ ] add the folder input handling using `webkitRelativePath`, the `FileList` cap, and the "Folder" button with its `webkitdirectory` input next to "Upload"
- [ ] run unit and e2e tests - must pass before next task

### Task 6: Verify acceptance criteria
- [ ] verify all requirements from Overview are implemented
- [ ] verify edge cases are handled: exact-size file accepted, excluded filename refused, nested exclude refused, in-root symlink accepted, symlinked ancestor outside root refused, no directory left behind on a validation-rejected request, concurrent mkdir, navigation during upload and during the refresh, Back navigation, 429 retry and exhaustion, HTML login response
- [ ] run full test suite: `go test -race ./...`
- [ ] run e2e tests: `make e2e`
- [ ] run `golangci-lint run ./...`
- [ ] verify the `server` package coverage from `go test -cover ./server/` is not below the branch-base value recorded in the progress file `/tmp/chat-plan-exec-folder-upload.txt`

### Task 7: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Move: `docs/plans/20260913-folder-upload.md` to `docs/plans/completed/`

- [ ] README.md: folder drop and the Folder button under File Upload; state that enabling upload permits directory creation under the root; reword the path-traversal bullet now that directories are created on demand; state that excludes apply to uploaded file names and directories; describe the per-file size limit and the aggregate body ceiling for multi-part requests; note that empty directories are not recreated and the 1000-file cap per selection; update the endpoint description so `path` may name a subdirectory that will be created
- [ ] CLAUDE.md: upload script location, per-file request model, the `data-` attribute handoff to the static script, the 429 retry and pacing rule, the 1 MB multipart buffer and `uploadOverheadBytes` in the upload architecture block; correct the "HTMX v2" line to the vendored 1.9.10
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- drag/drop a real directory in Chrome, Firefox and Safari; Playwright cannot synthesize it
- drop a large tree such as a `node_modules` directory and confirm the refusal toast appears without the browser hanging
- with `--upload.overwrite`, confirm that a dropped folder overwrites files under the same relative paths

**External system updates**:
- the internal instance that showed the select-all and checkbox-overlap symptoms runs v0.20.1; the fixes from v0.20.4 reach it only with an upgrade

Smells pre-check: 15 items fixed before save (handler split for gocyclo, ancestor walk named as a method, Stat instead of Lstat for symlinked ancestors, directory created only after all parts validate, filepath.Join over path.Join, maxFiles as a single constant, paste bound once, per-selection accounting, early cap inside the walk, request pacing under the shared limiter, deterministic 429 test, dir mode stated, CSS checkbox moved to the folder task, e2e port constants, CSP quote corrected)

Plan review: 15 items fixed (writeUploadError so Task 2 ends at complexity 12 not 16, duplicate-toast e2e updated and failures carry the server error text, window.weblistUpload named as the test seam, late-refresh route scoped by header so navigation is not held, Task 3 split into queue and resilience halves, toast strings fixed with the singular, comments cut to the two non-obvious, port 18085 assigned to the one new server, tasks renumbered, template snippet marked as end state with Task 3's share stated, aggregate-limit test labelled a regression guard, CSS dropped from the folder task, Files block added to the docs task, manual browser check moved out of the checklist, FIFO queue and root dest normalization and the coverage bar stated, htmx version corrected in context and docs task)
