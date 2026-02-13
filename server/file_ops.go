package server

import (
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// getTemplateFuncs returns the common template functions map
func (wb *Web) getTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
	}
}

// getSortParams retrieves sort parameters from query or cookies and returns them
// it also sets cookies if query parameters are provided
func (wb *Web) getSortParams(w http.ResponseWriter, r *http.Request) (sortBy, sortDir string) {
	// check query parameters first
	sortBy = r.URL.Query().Get("sort")
	sortDir = r.URL.Query().Get("dir")

	// handle parameters from query
	if sortBy != "" || sortDir != "" {
		return wb.processSortQueryParams(w, r, sortBy, sortDir)
	}

	// handle parameters from cookies
	return wb.getSortParamsFromCookies(r)
}

// processSortQueryParams processes and saves sort parameters from query
func (wb *Web) processSortQueryParams(w http.ResponseWriter, r *http.Request, sortBy, sortDir string) (resultSortBy, resultSortDir string) {
	// if either is set, ensure both have values
	if sortBy == "" {
		sortBy = "name" // default sort
	}
	if sortDir == "" {
		sortDir = "asc" // default direction
	}

	// set cookies with sorting preferences
	http.SetCookie(w, &http.Cookie{
		Name:     "sortBy",
		Value:    sortBy,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   60 * 60 * 24 * 365, // 1 year
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "sortDir",
		Value:    sortDir,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		MaxAge:   60 * 60 * 24 * 365, // 1 year
	})

	return sortBy, sortDir
}

// getSortParamsFromCookies gets sort parameters from cookies with defaults
func (wb *Web) getSortParamsFromCookies(r *http.Request) (sortBy, sortDir string) {
	// try to get from cookies
	if sortByCookie, err := r.Cookie("sortBy"); err == nil {
		sortBy = sortByCookie.Value
	}
	if sortDirCookie, err := r.Cookie("sortDir"); err == nil {
		sortDir = sortDirCookie.Value
	}

	// if still empty after checking cookies, use defaults
	if sortBy == "" {
		sortBy = "name" // default sort
	}
	if sortDir == "" {
		sortDir = "asc" // default direction
	}

	return sortBy, sortDir
}

// renderFullPage renders the complete HTML page
func (wb *Web) renderFullPage(w http.ResponseWriter, r *http.Request, path string) {
	// clean the path to avoid directory traversal attacks
	path = filepath.ToSlash(filepath.Clean(path))

	// check if the path exists
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		http.Error(w, fmt.Sprintf("path not found: %s - %v", path, err), http.StatusNotFound)
		return
	}

	// if it's not a directory, redirect to download handler
	if !fileInfo.IsDir() {
		http.Redirect(w, r, "/"+path, http.StatusSeeOther)
		return
	}

	// use cached template

	// get sort parameters from query or cookies
	sortBy, sortDir := wb.getSortParams(w, r)

	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		http.Error(w, "error reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
	}

	// check if user is authenticated (for showing logout button)
	isAuthenticated := false
	if wb.Auth != "" {
		isAuthenticated = wb.isAuthenticatedByCookie(r)
	}

	data := struct {
		Files             []FileInfo
		Path              string
		DisplayPath       string
		SortBy            string
		SortDir           string
		PathParts         []map[string]string
		Theme             string
		HideFooter        bool
		IsAuthenticated   bool
		Title             string
		BrandName         string
		BrandColor        string
		CustomFooter      string
		EnableMultiSelect bool
		EnableUpload      bool
		UploadMaxSize     int64
	}{
		Files:             fileList,
		Path:              path,
		DisplayPath:       displayPath,
		SortBy:            sortBy,
		SortDir:           sortDir,
		PathParts:         wb.getPathParts(path, sortBy, sortDir),
		Theme:             wb.Theme,
		HideFooter:        wb.HideFooter,
		IsAuthenticated:   isAuthenticated,
		Title:             wb.Title,
		BrandName:         wb.BrandName,
		BrandColor:        wb.BrandColor,
		CustomFooter:      wb.CustomFooter,
		EnableMultiSelect: wb.EnableMultiSelect,
		EnableUpload:      wb.EnableUpload,
		UploadMaxSize:     wb.UploadMaxSize,
	}

	// execute the entire template
	if err := wb.templates.indexTemplate.Execute(w, data); err != nil {
		http.Error(w, "template rendering error: "+err.Error(), http.StatusInternalServerError)
	}
}

// detectBinary checks if a file contains binary content, using cache for efficiency.
// cache key is path+mtime, so changed files get re-checked automatically.
func (wb *Web) detectBinary(fi *FileInfo) {
	if fi.IsDir {
		return
	}
	// fallback to direct detection if cache not initialized (e.g., in tests)
	if wb.binaryCache == nil {
		fi.detectBinaryContent(wb.FS)
		return
	}
	key := fmt.Sprintf("%s:%d", fi.Path, fi.LastModified.UnixNano())
	// error ignored: loader function never returns errors
	isBinary, _ := wb.binaryCache.Get(key, func() (bool, error) {
		return fi.detectBinaryContent(wb.FS), nil
	})
	fi.isBinary = isBinary
}

// getFileList returns a list of files in the specified directory
func (wb *Web) getFileList(path, sortBy, sortDir string) ([]FileInfo, error) {
	// get the list of files in the directory
	entries, err := fs.ReadDir(wb.FS, path)
	if err != nil {
		return nil, err
	}

	// convert the entries to FileInfo
	files := make([]FileInfo, 0, len(entries))

	// add a parent directory entry if we're not at the root
	if path != "." {
		parentPath := filepath.Dir(path)

		// create parent directory entry with minimal information
		parentEntry := FileInfo{
			Name:  "..",
			IsDir: true,
			Path:  parentPath,
			// LastModified intentionally omitted - will be zero value
			// this is better than showing incorrect time
		}

		// try to get the actual modification time of the parent directory
		// but don't fail if we can't - just leave LastModified as zero value
		if parentInfo, err := fs.Stat(wb.FS, parentPath); err == nil && parentInfo != nil {
			parentEntry.LastModified = parentInfo.ModTime()
		}

		files = append(files, parentEntry)
	}

	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())

		// skip excluded files and directories
		if wb.shouldExclude(entryPath) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("[WARN] failed to get info for %s: %v", entry.Name(), err)
			continue
		}

		lastModified := info.ModTime()
		// for directories, calculate recursive mtime if enabled
		if entry.IsDir() && wb.RecursiveMtime {
			if recursiveMtime := wb.getRecursiveMtime(entryPath); !recursiveMtime.IsZero() {
				lastModified = recursiveMtime
			}
		}

		fi := FileInfo{
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: lastModified,
			IsDir:        entry.IsDir(),
			Path:         entryPath,
		}
		wb.detectBinary(&fi)
		files = append(files, fi)
	}

	// sort the file list
	wb.sortFiles(files, sortBy, sortDir)

	return files, nil
}

// getRecursiveMtime returns the most recent modification time of any file
// within the directory tree. This is useful for sorting directories by
// when their content was last modified, not just direct children.
// Excluded files and directories are skipped to match the visible listing.
func (wb *Web) getRecursiveMtime(path string) time.Time {
	var newest time.Time
	_ = fs.WalkDir(wb.FS, path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors, continue walking
		}
		// skip excluded paths - for directories, skip the entire subtree
		if wb.shouldExclude(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // skip directories, only check files
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}

// shouldExclude checks if a path should be excluded based on the Exclude patterns.
// It performs several matching strategies:
// 1. Exact path match with the pattern
// 2. Any directory component matches the pattern (e.g. ".git" would exclude ".git/config")
// 3. Path ends with the pattern as a directory component
// All paths are normalized to use forward slashes for consistent matching across platforms.
func (wb *Web) shouldExclude(path string) bool {
	if len(wb.Exclude) == 0 {
		return false
	}

	// normalize path for matching
	normalizedPath := filepath.ToSlash(path)
	pathParts := strings.Split(normalizedPath, "/")

	for _, pattern := range wb.Exclude {
		// convert pattern to use forward slashes for consistency
		pattern = filepath.ToSlash(pattern)

		// check if the path matches the pattern exactly
		if normalizedPath == pattern {
			return true
		}

		// check if the path contains the pattern as a directory component
		// this handles cases like "some/git/path" when pattern is ".git"
		if slices.Contains(pathParts, pattern) {
			return true
		}

		// check if the path ends with the pattern
		if strings.HasSuffix(normalizedPath, "/"+pattern) {
			return true
		}
	}

	return false
}

// sortFiles sorts the file list based on the specified criteria (name, date, or size).
// The sort maintains several important properties:
// 1. The ".." parent directory entry always appears first
// 2. Directories are always grouped before files, regardless of sort field
// 3. When directories are sorted by size, they're sorted by name instead for consistency
// 4. Files are sorted by the requested field with case-insensitive name comparison
// 5. The sortDir parameter (asc/desc) reverses the sort order when set to "desc"
func (wb *Web) sortFiles(files []FileInfo, sortBy, sortDir string) {
	// first separate directories and files
	sort.SliceStable(files, func(i, j int) bool {
		// special case: ".." always comes first
		if files[i].Name == ".." {
			return true
		}
		if files[j].Name == ".." {
			return false
		}

		if files[i].IsDir && !files[j].IsDir {
			return true
		}
		if !files[i].IsDir && files[j].IsDir {
			return false
		}

		// both are directories or both are files
		var result bool

		// sort based on the specified field
		switch sortBy {
		case "name":
			result = strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		case "date":
			result = files[i].LastModified.Before(files[j].LastModified)
		case "size":
			if files[i].IsDir && files[j].IsDir {
				// if both are directories, sort by name in ascending order regardless of sortDir
				return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
			}
			result = files[i].Size < files[j].Size
		default:
			result = strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		}

		// reverse if descending order is requested
		if sortDir == "desc" {
			result = !result
		}

		return result
	})
}

// getPathParts splits a path into parts for breadcrumb navigation
func (wb *Web) getPathParts(path, sortBy, sortDir string) []map[string]string {
	if path == "." {
		return []map[string]string{}
	}

	// convert path separators to slashes for consistent handling
	path = filepath.ToSlash(path)

	parts := strings.Split(path, "/")
	result := make([]map[string]string, 0, len(parts))

	// build the breadcrumb parts
	var currentPath string
	for _, part := range parts {
		if part == "" {
			continue
		}

		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		result = append(result, map[string]string{
			"Name": part,
			"Path": currentPath,
			"Sort": sortBy,  // add sort parameter
			"Dir":  sortDir, // add direction parameter
		})
	}

	return result
}

// highlightCode applies syntax highlighting to the given code content
func (wb *Web) highlightCode(code, filename, theme string) (string, error) {
	// get lexer for the file
	lexer := lexers.Get(filename)
	if lexer == nil {
		// try to detect language from content if filename doesn't help
		lexer = lexers.Analyse(code)
		if lexer == nil {
			// fall back to plain text if no lexer found
			return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), nil
		}
	}

	// get style based on theme
	var style *chroma.Style
	if theme == "dark" {
		style = styles.Get("monokai")
	} else {
		style = styles.Get("github")
	}

	// create HTML formatter with line numbers
	formatter := html.New(html.WithClasses(true))

	// write HTML header
	var buf strings.Builder
	buf.WriteString(`<div class="highlight-wrapper">`)

	// tokenize and format the code
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), err
	}

	// format the tokens
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return fmt.Sprintf(`<div class="highlight-wrapper"><pre class="chroma">%s</pre></div>`, template.HTMLEscapeString(code)), err
	}

	// write HTML footer
	buf.WriteString("</div>")

	return buf.String(), nil
}
