# Go-pkgz/testutils Commands and Guidelines

## Build/Test/Lint Commands
- Build: `go build -race`
- Test all packages: `go test -race -covermode=atomic -coverprofile=profile.cov ./...`
- Run single test: `go test -run=TestName ./path/to/package`
- Run test in verbose mode: `go test -v -run=TestName ./path/to/package`
- Lint: `golangci-lint run`

## Code Style Guidelines
- Use camelCase for functions and variables, with clear descriptive names
- Test files use `_test.go` suffix with table-driven tests using anonymous structs
- Pass context as first parameter when relevant
- Use t.Helper() in test utility functions for better error reporting
- Employ t.Run() for subtests with descriptive names
- Always defer resource cleanup in tests and production code
- Return errors rather than panicking; handle errors explicitly
- Include t.Skip() for integration tests when testing.Short() is true
- Prefer testify's require/assert package for test assertions
- Use proper Go modules import ordering (standard lib, external, internal)
- Always close resources using defer pattern after creation (eg. defer container.Close(ctx))