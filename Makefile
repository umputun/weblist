# Get the latest commit branch, hash, and date
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
TIMESTAMP=$(shell git log -1 --format=%ct HEAD 2>/dev/null | xargs -I{} date -u -r {} +%Y%m%dT%H%M%S)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

all: test build

build:
	go build -ldflags "-s -w" -o .bin/weblist

test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	grep -v "_mock.go" coverage.out | grep -v mocks > coverage_no_mocks.out
	go tool cover -func=coverage_no_mocks.out
	rm coverage.out coverage_no_mocks.out

race_test:
	go test -race -timeout=60s -count 1 ./...

lint:
	golangci-lint run ./...

docker:
	docker build -t umputun/weblist .

release:
	goreleaser --snapshot --skip=publish --clean

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

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

.PHONY: all build test race_test lint docker release version prep_site e2e-setup e2e e2e-ui
