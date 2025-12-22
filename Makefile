prep_site:
	cp -fv README.md site/docs/index.md
	sed -i '' 's|https:\/\/github.com\/umputun\/weblist\/raw\/master\/site\/docs\/logo.png|logo.png|' site/docs/index.md
	sed -i '' 's|^.*/workflows/ci.yml.*$$||' site/docs/index.md

# install playwright browsers (run once or after playwright-go version update)
e2e-setup:
	go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium

# run e2e tests headless (default, for CI and quick checks)
e2e:
	go test -v -count=1 -timeout=5m -tags=e2e ./e2e/...

# run e2e tests with visible UI (for debugging and development)
e2e-ui:
	E2E_HEADLESS=false go test -v -count=1 -timeout=10m -tags=e2e ./e2e/...

.PHONY: prep_site e2e-setup e2e e2e-ui