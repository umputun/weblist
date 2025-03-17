# Contributing to Weblist

Thank you for your interest in contributing to Weblist! This document outlines the guidelines for contributing to the project.

## Development Workflow

### Before Making Changes

1. Fork the repository and clone it locally
2. Create a new branch for your changes
3. Install dependencies with `go mod download`

### Making Changes

1. Write your code following the Go style guidelines
2. Add tests for new functionality
3. Update documentation as needed

### Before Committing

**IMPORTANT**: Always follow these steps before committing:

1. Run the linter to ensure code quality:
   ```bash
   golangci-lint run
   ```

2. Run tests to ensure everything works:
   ```bash
   go test ./...
   ```

3. Fix any issues reported by the linter or tests

### Committing Changes

**IMPORTANT**: Never commit changes unless explicitly asked to do so by the project maintainer.

When asked to commit:
1. Make sure linting and tests pass
2. Use descriptive commit messages
3. Reference issue numbers in commit messages when applicable

## Code Style

This project uses `golangci-lint` with the configuration in `.golangci.yml`. The linter enforces:

- Code formatting with `gofmt`
- Code quality with `staticcheck`, `gosec`, and others
- Style consistency with `stylecheck` and `revive`

## Testing

All new functionality should include tests. Run tests with:

```bash
go test ./...
```

For more verbose output:

```bash
go test -v ./...
```

## Documentation

Update documentation when adding new features or changing existing ones:

- Update README.md for user-facing changes
- Update code comments for developer-facing changes
- Add examples when appropriate

## Pull Requests

When submitting a pull request:

1. Ensure all linting and tests pass
2. Provide a clear description of the changes
3. Link to any related issues
4. Be responsive to feedback and questions

Thank you for contributing to Weblist! 