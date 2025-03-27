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
- `--brand-name`: Company or organization name to display in navbar - env: `BRAND_NAME`
- `--brand-color`: Color for navbar and footer (e.g. `#3498db`) - env: `BRAND_COLOR`
- `--sftp`: Enable SFTP server with specified username - env: `SFTP`
- `--sftp-address`: Address for SFTP server (default: `:2022`) - env: `SFTP_ADDRESS`
- `--sftp-key`: SSH private key file (default: `weblist_rsa`) - env: `SFTP_KEY`
- `--sftp-authorized`: Path to OpenSSH authorized_keys file for public key authentication - env: `SFTP_AUTHORIZED`
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
# Enable SFTP with password authentication
weblist --auth your_password --sftp sftp_user

# Use a custom SFTP port
weblist --auth your_password --sftp sftp_user --sftp-address :2222

# Specify a custom SSH host key file
weblist --auth your_password --sftp sftp_user --sftp-key /path/to/ssh_key

# Enable SFTP with public key authentication (no password needed)
weblist --sftp sftp_user --sftp-authorized /path/to/authorized_keys
```

When SFTP is enabled:
- The same directory is served via SFTP and HTTP
- File exclusions apply to both HTTP and SFTP
- Authentication can use either:
  - Password authentication: Uses the same password as HTTP authentication (requires `--auth` parameter)
  - Public key authentication: Uses OpenSSH-format authorized_keys file (requires `--sftp-authorized` parameter)
- The username for SFTP is specified with the `--sftp` parameter
- SFTP access is read-only for security reasons
- SSH host keys are stored to prevent client warnings about changing keys
  - By default, the key is stored as `weblist_rsa` in the current directory
  - You can specify a custom key file with `--sftp-key`

SFTP support is optional and only enabled when the `--sftp` parameter is provided. Either the `--auth` or `--sftp-authorized` parameter is required when enabling SFTP.

## Custom Branding

Weblist allows you to customize the appearance with your organization's branding:

```bash
# Set a custom organization name in the navigation bar
weblist --brand-name "My Company"

# Set a custom color for the navigation bar and footer
weblist --brand-color "#3498db"

# Combine both branding options
weblist --brand-name "My Company" --brand-color "#3498db"
```

When branding is enabled:
- Your organization name appears in the navigation bar
- The specified color is applied to both the navigation bar and footer
- The branding is consistently displayed across all pages
- These settings can be combined with all other Weblist options

Custom branding is optional and only activated when the `--brand-name` and/or `--brand-color` parameters are provided.

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
      - AUTH=your_password  # Optional: Enable password authentication
      - BRAND_NAME=My Company  # Optional: Display company name in navbar
      - BRAND_COLOR=#3498db  # Optional: Custom color for navbar and footer
      - SFTP=sftp_user      # Optional: Enable SFTP access
      - SFTP_ADDRESS=:2022  # Optional: SFTP port
      - SFTP_KEY=/data/ssh_key  # Optional: Path to SSH host key
      - SFTP_AUTHORIZED=/data/authorized_keys  # Optional: Path to authorized_keys file for public key auth
```
