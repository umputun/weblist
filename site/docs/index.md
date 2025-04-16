

A modern, elegant file browser for the web. Weblist provides a clean and intuitive interface for browsing and downloading files from any directory on your server, replacing the ugly default directory listings of Nginx and Apache with a beautiful, functional alternative.

<div align="center">
  <img class="logo" src="logo.png" width="400px" alt="Weblist"/>
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
- **Syntax Highlighting**: Beautiful code highlighting for various programming languages (optional)
- **JSON API**: Programmatic access to file listings via a simple JSON API

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

# Enable syntax highlighting for code files
weblist --syntax-highlight
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
- `--auth-user`: Username for authentication (default: `weblist`) - env: `AUTH_USER`
- `--session-secret`: Secret key for session tokens (auto-generated if not set) - env: `SESSION_SECRET`
- `--session-ttl`: Session timeout duration (default: `24h`) - env: `SESSION_TTL`
- `--insecure-cookies`: Allow cookies without secure flag - env: `INSECURE_COOKIES`
- `-v, --version`: Show version and exit - env: `VERSION`
- `--dbg`: Debug mode - env: `DEBUG`
- `--syntax-highlight`: Enable syntax highlighting for code files - env: `SYNTAX_HIGHLIGHT`
- `--custom-footer`: Custom footer text (can contain HTML) - env: `CUSTOM_FOOTER`

SFTP Options (with `--sftp` prefix):
- `--sftp.enabled`: Enable SFTP server - env: `SFTP_ENABLED`
- `--sftp.user`: Username for SFTP access - env: `SFTP_USER`
- `--sftp.address`: Address for SFTP server (default: `:2022`) - env: `SFTP_ADDRESS`
- `--sftp.key`: SSH private key file (default: `weblist_rsa`) - env: `SFTP_KEY`
- `--sftp.authorized`: Path to OpenSSH authorized_keys file for public key authentication - env: `SFTP_AUTHORIZED`

Branding Options (with `--brand` prefix):
- `--brand.name`: Company or organization name to display in navbar - env: `BRAND_NAME`
- `--brand.color`: Color for navbar (e.g. `3498db` or `#3498db`) - env: `BRAND_COLOR`

## Authentication

Weblist provides optional password protection for your file listings:

```bash
# Enable authentication with a password
weblist --auth your_password

# Customize the username (default is "weblist")
weblist --auth your_password --auth-user admin

# Set a specific session secret key
weblist --auth your_password --session-secret "your-secret-key"

# Change session timeout (default is 24 hours)
weblist --auth your_password --session-ttl 12h

# Enable on non-HTTPS servers or development (not recommended for production)
weblist --auth your_password --insecure-cookies
```

When authentication is enabled:
- Users will be prompted with a login screen
- The username is customizable via the `--auth-user` parameter (defaults to "weblist")
- The password is whatever you specify with the `--auth` parameter
- Sessions are secured with HMAC-SHA256 signed tokens
- Session secrets are automatically generated if not explicitly provided
- Sessions expire after the specified timeout (default: 24 hours)
- A logout button appears in the top right corner when logged in

Authentication is completely optional and only activated when the `--auth` parameter is provided.

## SFTP Access

Weblist can also provide SFTP access to the same files:

```bash
# Enable SFTP with password authentication
weblist --auth your_password --sftp.enabled --sftp.user sftp_user

# Use a custom SFTP port
weblist --auth your_password --sftp.enabled --sftp.user sftp_user --sftp.address :2222

# Specify a custom SSH host key file
weblist --auth your_password --sftp.enabled --sftp.user sftp_user --sftp.key /path/to/ssh_key

# Enable SFTP with public key authentication (no password needed)
weblist --sftp.enabled --sftp.user sftp_user --sftp.authorized /path/to/authorized_keys
```

When SFTP is enabled:
- The same directory is served via SFTP and HTTP
- File exclusions apply to both HTTP and SFTP
- Authentication can use either:
  - Password authentication: Uses the same password as HTTP authentication (requires `--auth` parameter)
  - Public key authentication: Uses OpenSSH-format authorized_keys file (requires `--sftp.authorized` parameter)
- The username for SFTP is specified with the `--sftp.user` parameter
- SFTP access is read-only for security reasons
- SSH host keys are stored to prevent client warnings about changing keys
  - By default, the key is stored as `weblist_rsa` in the current directory
  - You can specify a custom key file with `--sftp.key`

SFTP support is optional and only enabled when both `--sftp.enabled` and `--sftp.user` parameters are provided. Either the `--auth` or `--sftp.authorized` parameter is required when enabling SFTP.

## Custom Branding

Weblist allows you to customize the appearance with your organization's branding:

```bash
# Set a custom organization name in the navigation bar
weblist --brand.name "My Company"

# Set a custom color for the navigation bar (with or without # prefix)
weblist --brand.color "#3498db"
# or
weblist --brand.color "3498db"

# Use a custom footer text (can contain HTML)
weblist --custom-footer "Powered by <a href='https://example.com'>Example</a> | © 2025"

# Combine branding options
weblist --brand.name "My Company" --brand.color "#3498db" --custom-footer "© 2025 My Company"
```

When branding is enabled:
- Your organization name appears in the navigation bar
- The specified color is applied to the navigation bar
- Custom footer text replaces the default footer links (allows HTML links)
- The branding is consistently displayed across all pages
- These settings can be combined with all other Weblist options

Custom branding is optional and only activated when the related parameters are provided.

## JSON API

Weblist provides a JSON API for programmatic access to file listings:

```
GET /api/list?path=path/to/directory&sort=+name
```

### API Parameters

- `path`: The directory path to list (default: root directory)
- `sort`: The sort criteria with direction prefix (optional, default: `+name`):
  - `+name` or `-name`: Sort by name (ascending or descending)
  - `+size` or `-size`: Sort by file size (ascending or descending)
  - `+mtime` or `-mtime`: Sort by modification time (ascending or descending)

### Example Request

```
GET /api/list?path=docs&sort=-size
```

### Example Response

```json
{
  "path": "docs",
  "files": [
    {
      "name": "..",
      "path": ".",
      "is_dir": true,
      "size": 0,
      "size_human": "-",
      "last_modified": "2023-01-01T12:00:00Z",
      "time_str": "01-Jan-2023 12:00:00",
      "is_viewable": false
    },
    {
      "name": "images",
      "path": "docs/images",
      "is_dir": true,
      "size": 0,
      "size_human": "-",
      "last_modified": "2023-01-01T12:00:00Z",
      "time_str": "01-Jan-2023 12:00:00",
      "is_viewable": false
    },
    {
      "name": "document.pdf",
      "path": "docs/document.pdf",
      "is_dir": false,
      "size": 1048576,
      "size_human": "1.0M",
      "last_modified": "2023-01-01T12:00:00Z",
      "time_str": "01-Jan-2023 12:00:00",
      "is_viewable": true
    }
  ],
  "sort": "size",
  "dir": "desc"
}
```

The JSON API provides all the same functionality as the web interface, including respecting exclusion rules, authentications, and sorting preferences.

### Accessing Files

To download or access individual files, you can use a simple GET request to the file path:

```
GET /{path/to/file}
```

For example:
```
GET /docs/document.pdf
```

This will:
- Return the file with the appropriate Content-Type header
- Set Content-Disposition to "attachment" for binary files
- Allow direct viewing in the browser for compatible file types (text, HTML, images, PDFs)

The path format matches exactly what's returned in the `path` field of the JSON API response.

For viewing text files or code in the browser with syntax highlighting (when enabled):
```
GET /view/{path/to/file}
```

All file access respects the same authentication and exclusion rules as the web interface.

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
      - AUTH_USER=admin  # Optional: Custom username for authentication (default: weblist)
      - SESSION_SECRET=your_secure_key  # Optional: Secret for signing session tokens
      - SESSION_TTL=24h  # Optional: Session timeout duration
      - BRAND_NAME=My Company  # Optional: Display company name in navbar
      - BRAND_COLOR=#3498db  # Optional: Custom color for navbar
      - CUSTOM_FOOTER="<a href='https://example.com'>Example</a> | © 2025"  # Optional: Custom footer text
      - SFTP_ENABLED=true   # Optional: Enable SFTP server
      - SFTP_USER=sftp_user # Optional: Username for SFTP access
      - SFTP_ADDRESS=:2022  # Optional: SFTP port
      - SFTP_KEY=/data/ssh_key  # Optional: Path to SSH host key
      - SFTP_AUTHORIZED=/data/authorized_keys  # Optional: Path to authorized_keys file for public key auth
      - SYNTAX_HIGHLIGHT=true  # Optional: Enable syntax highlighting for code files
```
