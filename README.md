# weblist [![build](https://github.com/umputun/weblist/actions/workflows/ci.yml/badge.svg)](https://github.com/umputun/weblist/actions/workflows/ci.yml)

A modern, elegant file browser for the web. Weblist provides a clean and intuitive interface for browsing and downloading files from any directory on your server, replacing the ugly default directory listings of Nginx and Apache with a beautiful, functional alternative.

## Why weblist?

- **Clean, Modern Interface**: Simple design that improves readability and usability
- **Security First**: Locked to the root directory - users can't access files outside the specified folder
- **Intuitive Navigation**: Simple breadcrumb navigation makes it easy to move between directories
- **Smart Sorting**: Sort files by name, size, or date with a single click
- **Mobile Friendly**: Works great on phones and tablets, not just desktops
- **Fast & Lightweight**: Loads quickly even on slow connections
- **No Setup Required**: Single binary that just works - no configuration needed
- **Dark Mode**: Easy on the eyes with both light and dark themes

## Quick Start

```bash
# Serve the current directory
weblist

# Serve a specific directory
weblist --root /path/to/files

# Use dark theme
weblist --theme dark

# Specify a different port
weblist --listen :9000
```

## Installation

### Download Binary

Download the latest release from the [releases page](https://github.com/umputun/weblist/releases).

### Using Go

```bash
go install github.com/umputun/weblist@latest
```

## Usage

```
weblist [options]
```

### Options

- `-l, --listen`: Address to listen on (default: `:8080`)
- `-t, --theme`: Theme to use, "light" or "dark" (default: `light`)
- `-r, --root`: Root directory to serve (default: current directory)
- `-f, --hide-footer`: Hide footer
- `-v, --version`: Show version and exit
- `--dbg`: Debug mode

## Docker

Perfect for NAS devices or home servers:

```bash
docker run -p 8080:8080 -v /path/to/files:/srv umputun/weblist
```

### Docker Compose

```yaml
version: '3'
services:
  weblist:
    image: umputun/weblist
    container_name: weblist
    restart: always
    ports:
      - "8080:8080"
    volumes:
      - /path/to/files:/srv
    environment:
      - LISTEN=:8080
      - THEME=light
      - ROOT_DIR=/srv
```

## License

MIT

## Author

[Umputun](https://github.com/umputun) 