# weblist [![build](https://github.com/umputun/weblist/actions/workflows/ci.yml/badge.svg)](https://github.com/umputun/weblist/actions/workflows/ci.yml)

A lightweight, modern file server that provides a clean web interface for browsing and downloading files from a directory. Built with Go and HTMX for a responsive, single-page experience without complex JavaScript frameworks.

## Features

- **Clean, Modern UI**: Minimalist design with light and dark themes
- **Responsive Layout**: Works well on desktop and mobile devices
- **HTMX-Powered Navigation**: Smooth directory browsing without page reloads
- **File Downloads**: Easy access to files with direct download links
- **Sorting Options**: Sort by name, size, or modification date
- **Breadcrumb Navigation**: Easy navigation through directory structure
- **Directory Traversal Protection**: Secure by design, prevents access outside the root directory
- **Human-Readable File Sizes**: Displays file sizes in a user-friendly format

## Installation

### From Source

```bash
git clone https://github.com/umputun/weblist.git
cd weblist
go build
```

### Using Go Install

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

### Examples

Serve the current directory on the default port:
```bash
weblist
```

Serve a specific directory on a custom port with dark theme:
```bash
weblist --root /path/to/files --listen :9000 --theme dark
```

## Docker

You can run weblist in a Docker container:

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

## Screenshots

![Light Theme](https://github.com/umputun/weblist/raw/master/docs/screenshots/light-theme.png)
![Dark Theme](https://github.com/umputun/weblist/raw/master/docs/screenshots/dark-theme.png)

## How It Works

Weblist is built with Go and uses the following technologies:

- **Go's Standard Library**: For the HTTP server and file system operations
- **HTMX**: For dynamic content loading without full page reloads
- **Pico CSS**: For lightweight, semantic styling
- **Go Templates**: For server-side rendering

The application embeds all assets and templates, resulting in a single binary that's easy to deploy.

## Development

### Prerequisites

- Go 1.21 or higher

### Building

```bash
go build
```

### Running Tests

```bash
go test ./...
```

### Running with Hot Reload

For development, you can use [air](https://github.com/cosmtrek/air) for hot reloading:

```bash
air
```

## License

MIT

## Author

[Umputun](https://github.com/umputun) 