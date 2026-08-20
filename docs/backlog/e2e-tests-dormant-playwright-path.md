---
worth: later
where: go.mod:17
added: 2026-08-19
---
# the e2e tests cannot run until playwright-go is migrated

`.github/workflows/e2e.yml` was removed in 4b6550c because e2e rarely earns a CI slot. The 51 tests
in `e2e/` were kept and `make e2e` still exists, but neither works today.

`go.mod` requires `github.com/playwright-community/playwright-go v0.5200.1`. That version pins
Playwright CLI 1.52.0 and downloads its driver from the three azureedge mirrors listed in its
`run.go`, building `{mirror}/builds/driver/playwright-1.52.0-linux.zip`. All three return 404 now,
so a machine without a cached driver cannot install it. A local run appears to work only if
`~/.cache/ms-playwright-go/1.52.0` (or the macOS `~/Library/Caches` equivalent) is already
populated from before.

The module has moved to `github.com/mxschmitt/playwright-go`, and current versions fetch from the
npm registry instead, so they still install. Releases v0.6000.0 and later exist under both paths but
declare `mxschmitt`, which is why `@latest` on the old path fails with a module path mismatch.

Fix is the same migration paskal did in umputun/ralphex#434: change the requirement in `go.mod`, the
imports in `e2e/e2e_test.go` and `e2e/view_test.go`, and the install command in `Makefile:44`, which
still floats at `@latest`. Pin that command to an explicit version rather than `@latest`, since
floating is what broke it.

Only worth doing when someone next wants to run the tests. Nothing in CI depends on them.
