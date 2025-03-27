# weblist [![build](https://github.com/umputun/weblist/actions/workflows/ci.yml/badge.svg)](https://github.com/umputun/weblist/actions/workflows/ci.yml) &nbsp;[![Coverage Status](https://coveralls.io/repos/github/umputun/weblist/badge.svg?branch=master)](https://coveralls.io/github/umputun/weblist?branch=master)

A modern, elegant file browser for the web. Weblist provides a clean and intuitive interface for browsing and downloading files from any directory on your server, replacing the ugly default directory listings of Nginx and Apache with a beautiful, functional alternative.

<div align="center">
  <img class="logo" src="https://github.com/umputun/weblist/raw/master/site/docs/logo.png" width="400px" alt="Weblist"/>
</div>

## Why weblist?

- **Clean, Modern Interface**: Simple design that improves readability and usability
- **Security First**: Locked to the root directory - users can't access files outside the specified folder
- **Intuitive Navigation**: Simple breadcrumb navigation makes it easy to move between directories
- **Smart Sorting**: Sort files by name, size, or date with a single click
- **Mobile Friendly**: Works great on phones and tablets, not just desktops
- **Fast & Lightweight**: Loads quickly even on slow connections
- **No Setup Required**: Single binary that just works - no configuration needed
- **Dark Mode**: Easy on the eyes with both light and dark themes
- **Optional Authentication**: Password-protect your file listings when needed
- **SFTP Support**: Access the same files via SFTP for more advanced operations

<details markdown>
  <summary>Screenshots</summary>

![dark-theme](https://github.com/umputun/weblist/raw/master/site/docs/screen-dark.png)

![dark-theme](https://github.com/umputun/weblist/raw/master/site/docs/screen-light.png)

![dark-theme](https://github.com/umputun/weblist/raw/master/site/docs/screen-preview.png)

![dark-theme](https://github.com/umputun/weblist/raw/master/site/docs/screen-login.png)

</details>


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

# Exclude specific files and directories
weblist --exclude .git --exclude .env

# Enable password protection
weblist --auth your_password
```

## Installation

### Download Binary

Download the latest release from the [releases page](https://github.com/umputun/weblist/releases).

### Using Go

```bash
go install github.com/umputun/weblist@latest
```

**Install from homebrew (macOS)**

```bash
brew tap umputun/apps
brew install umputun/apps/weblist
```

## Usage

```
weblist [options]
```

### Options

- `-l, --listen`: Address to listen on (default: `:8080`) - env: `LISTEN`
- `-t, --theme`: Theme to use, "light" or "dark" (default: `light`) - env: `THEME`
- `-r, --root`: Root directory to serve (default: current directory) - env: `ROOT_DIR`
- `-e, --exclude`: Files and directories to exclude (can be repeated) - env: `EXCLUDE`
- `-f, --hide-footer`: Hide footer - env: `HIDE_FOOTER`
- `-a, --auth`: Enable authentication with the specified password - env: `AUTH`
- `--sftp`: Enable SFTP server with specified username - env: `SFTP`
- `--sftp-address`: Address for SFTP server (default: `:2022`) - env: `SFTP_ADDRESS`
- `--sftp-key`: SSH private key file (if empty, uses './weblist_rsa' or generates a new one) - env: `SFTP_KEY`
- `-v, --version`: Show version and exit - env: `VERSION`
- `--dbg`: Debug mode - env: `DEBUG`

## Authentication

Weblist provides optional password protection for your file listings:

```bash
# Enable authentication with a password
weblist --auth your_password
```

When authentication is enabled:
- Users will be prompted with a login screen
- The username is always "weblist" (hardcoded)
- The password is whatever you specify with the `--auth` parameter
- Sessions last for 24 hours by default
- A logout button appears in the top right corner when logged in

Authentication is completely optional and only activated when the `--auth` parameter is provided.

## SFTP Access

Weblist can also provide SFTP access to the same files:

```bash
# Enable SFTP with a username (password from --auth parameter is used)
weblist --auth your_password --sftp sftp_user

# Use a custom SFTP port
weblist --auth your_password --sftp sftp_user --sftp-address :2222

# Specify a custom SSH key file
weblist --auth your_password --sftp sftp_user --sftp-key /path/to/ssh_key
```

When SFTP is enabled:
- The same directory is served via SFTP and HTTP
- File exclusions apply to both HTTP and SFTP
- The same password is used for both HTTP and SFTP authentication
- The username for SFTP is specified with the `--sftp` parameter
- SFTP access is read-only for security reasons
- SSH host keys are stored to prevent client warnings about changing keys
  - By default, the key is stored as `weblist_rsa` in the current directory
  - You can specify a custom key file with `--sftp-key`

SFTP support is optional and only enabled when the `--sftp` parameter is provided. The `--auth` parameter is required when enabling SFTP.

## Docker

Perfect for NAS devices or home servers:

```bash
docker run -p 8080:8080 -v /path/to/files:/data umputun/weblist
```

### Docker Compose

```yaml
version: '3'
services:
  weblist:
    image: ghcr.io/umputun/weblist:latest
    container_name: weblist
    restart: always
    ports:
      - "8080:8080"
      - "2022:2022"  # SFTP port
    volumes:
      - /path/to/files:/data:ro
    environment:
      - LISTEN=:8080
      - THEME=light
      - ROOT_DIR=/data
      - EXCLUDE=.git,.env
      - AUTH=your_password  # Optional: Enable authentication
      - SFTP=sftp_user      # Optional: Enable SFTP access
      - SFTP_ADDRESS=:2022  # Optional: SFTP port
      - SFTP_KEY=/data/ssh_key  # Optional: Path to SSH host key
```
