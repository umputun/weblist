package server

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// getTemplateFuncs returns the common template functions map
func (wb *Web) getTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"safe": func(s string) template.HTML {
			return template.HTML(s) // nolint:gosec // safe to use with local embedded templates
		},
		"contains": strings.Contains,
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
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows the transport, plain HTTP is a supported deployment
		Name:     "sortBy",
		Value:    sortBy,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 365, // 1 year
	})

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows the transport, plain HTTP is a supported deployment
		Name:     "sortDir",
		Value:    sortDir,
		Path:     "/",
		HttpOnly: true,
		Secure:   wb.isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
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

	// check if the directory itself should be excluded, otherwise its content would be listed
	if wb.shouldExclude(path) {
		http.Error(w, "access denied to requested path", http.StatusForbidden)
		return
	}

	// check if the path exists
	fileInfo, err := fs.Stat(wb.FS, path)
	if err != nil {
		log.Printf("[DEBUG] path not found: %s: %v", path, err)
		http.Error(w, "path not found: "+path, http.StatusNotFound)
		return
	}

	// if it's not a directory, redirect to download handler
	if !fileInfo.IsDir() {
		// built through url.URL so a name containing # or ? is escaped rather than
		// starting a fragment or query
		download := url.URL{Path: "/" + path}
		http.Redirect(w, r, download.String(), http.StatusSeeOther)
		return
	}

	// use cached template

	// get sort parameters from query or cookies
	sortBy, sortDir := wb.getSortParams(w, r)

	fileList, err := wb.getFileList(path, sortBy, sortDir)
	if err != nil {
		log.Printf("[WARN] error reading directory %s: %v", path, err)
		http.Error(w, "error reading directory", http.StatusInternalServerError)
		return
	}

	// create a display path that looks nicer in the UI
	displayPath := path
	if path == "." {
		displayPath = ""
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
		CanUpload         bool
		ShowLogin         bool
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
		IsAuthenticated:   wb.isAuthenticated(r),
		Title:             wb.Title,
		BrandName:         wb.BrandName,
		BrandColor:        wb.BrandColor,
		CustomFooter:      wb.CustomFooter,
		EnableMultiSelect: wb.EnableMultiSelect,
		EnableUpload:      wb.EnableUpload,
		CanUpload:         wb.canUpload(r),
		ShowLogin:         wb.showLogin(r),
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
func (wb *Web) shouldExclude(path string) bool {
	return matchesExcludes(path, wb.Exclude)
}

// matchesExcludes reports whether the path matches any of the exclusion patterns. A pattern matches
// a path equal to it, and it matches wherever the pattern components appear as a contiguous run of
// path components, which covers everything below the match: ".git" excludes "some/.git" along with
// "some/.git/config", and "docs/private" excludes "docs/private" along with everything beneath it.
// Matching works on whole components, so "docs/private" leaves "docs/private2" alone and "vendor"
// leaves "vendors" alone. All paths are normalized to forward slashes for consistent matching
// across platforms. Comparison is case-insensitive: on case-insensitive filesystems (macOS, Windows)
// an exact match would let "secret" through when "Secret" is excluded, serving the file the operator
// meant to hide.
func matchesExcludes(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	normalizedPath := filepath.ToSlash(path)
	pathParts := pathComponents(path)
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if strings.EqualFold(normalizedPath, pattern) {
			return true
		}

		patternParts := pathComponents(pattern)
		if len(patternParts) == 0 {
			continue
		}
		for i := 0; i+len(patternParts) <= len(pathParts); i++ {
			if slices.EqualFunc(pathParts[i:i+len(patternParts)], patternParts, strings.EqualFold) {
				return true
			}
		}
	}

	return false
}

// pathComponents splits a path into its significant components, dropping empty and "." elements
// so that "docs/private", "./docs/private/" and "docs//private" all yield the same result.
func pathComponents(path string) []string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	res := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		res = append(res, part)
	}
	return res
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

// renderCSV parses CSV content incrementally and renders it as an HTML table.
// the first row is treated as a header. output is capped at maxCSVRows data rows.
// rows beyond the limit are counted but not stored, avoiding unbounded memory allocation.
// all cell values are HTML-escaped.
func (wb *Web) renderCSV(content string) (string, error) {
	const maxCSVRows = 1000

	reader := csv.NewReader(strings.NewReader(content))
	reader.FieldsPerRecord = -1 // allow variable number of fields

	// read header row
	header, err := reader.Read()
	if err != nil {
		return "", fmt.Errorf("csv parse: %w", err)
	}

	var buf strings.Builder
	buf.WriteString(`<div class="csv-content"><table>`)

	// render header
	buf.WriteString("<thead><tr>")
	for _, cell := range header {
		buf.WriteString("<th>")
		buf.WriteString(template.HTMLEscapeString(cell))
		buf.WriteString("</th>")
	}
	buf.WriteString("</tr></thead>")

	// render body rows incrementally, only store rendered output
	buf.WriteString("<tbody>")
	totalDataRows := 0
	for {
		row, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("csv parse: %w", err)
		}
		totalDataRows++
		if totalDataRows > maxCSVRows {
			continue // count remaining rows but don't render
		}
		buf.WriteString("<tr>")
		for _, cell := range row {
			buf.WriteString("<td>")
			buf.WriteString(template.HTMLEscapeString(cell))
			buf.WriteString("</td>")
		}
		buf.WriteString("</tr>")
	}
	buf.WriteString("</tbody>")

	buf.WriteString("</table>")

	if totalDataRows > maxCSVRows {
		fmt.Fprintf(&buf, `<p class="csv-truncated">showing %d of %d rows</p>`, maxCSVRows, totalDataRows)
	}

	buf.WriteString("</div>")

	return buf.String(), nil
}

// renderMarkdown converts markdown content to HTML using goldmark with GFM extensions.
// the theme parameter controls the chroma style for fenced code blocks: "dark" uses monokai, any other value uses github.
// highlight false renders fenced code blocks as plain code, bounding chroma's cost on large files.
// the output is wrapped in a <div class="markdown-content"> element.
func (wb *Web) renderMarkdown(content, theme string, highlight bool) (string, error) {
	chromaStyle := "github"
	if theme == "dark" {
		chromaStyle = "monokai"
	}

	// extension.GFM with its table extension rebuilt around tableCellLimit, at the same priorities
	limit := &tableCellLimit{inner: extension.NewTableParagraphTransformer()}
	extensions := []goldmark.Extender{extension.Linkify, extension.Strikethrough, extension.TaskList}
	if highlight {
		extensions = append(extensions, highlighting.NewHighlighting(
			highlighting.WithStyle(chromaStyle),
			highlighting.WithFormatOptions(html.WithClasses(true)),
		))
	}
	md := goldmark.New(
		goldmark.WithExtensions(extensions...),
		goldmark.WithParserOptions(
			parser.WithParagraphTransformers(util.Prioritized(limit, 200)),
			parser.WithASTTransformers(util.Prioritized(extension.NewTableASTTransformer(), 0)),
		),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(extension.NewTableHTMLRenderer(), 500))),
	)

	var buf strings.Builder
	buf.WriteString(`<div class="markdown-content">`)
	if err := md.Convert([]byte(content), &buf); err != nil {
		return "", fmt.Errorf("markdown conversion: %w", err)
	}
	if limit.exceeded {
		return "", fmt.Errorf("markdown tables exceed %d cells", maxMarkdownTableCells)
	}
	buf.WriteString("</div>")

	return buf.String(), nil
}

// maxMarkdownTableCells bounds the table cells one Markdown preview may build. goldmark pads every row
// to the header width, so a wide header over short rows costs cells quadratic in the file size.
const maxMarkdownTableCells = 50_000

// tableCellLimit wraps goldmark's table paragraph transformer and stops building tables once their cells
// pass maxMarkdownTableCells. It counts the lines goldmark hands the table transformer, after blockquote
// and list markers are removed, so its count is never below what that transformer would build.
type tableCellLimit struct {
	inner    parser.ParagraphTransformer
	cells    int64
	exceeded bool
}

// Transform counts the cells the widest delimiter row of the paragraph would make, header included,
// and passes the paragraph on only while the total stays within the limit.
func (t *tableCellLimit) Transform(node *gast.Paragraph, reader text.Reader, pc parser.Context) {
	if t.exceeded {
		return
	}
	lines := node.Lines()
	var most int64
	for i := 1; i < lines.Len(); i++ {
		seg := lines.At(i)
		most = max(most, int64(t.delimColumns(seg.Value(reader.Source())))*int64(lines.Len()-i))
	}
	if t.cells += most; t.cells > maxMarkdownTableCells {
		t.exceeded = true
		return
	}
	t.inner.Transform(node, reader, pc)
}

// delimColumns returns the column count goldmark would read from a delimiter row, or 0 for a line
// that cannot be one. It accepts every line goldmark's isTableDelim does, and some it does not.
func (t *tableCellLimit) delimColumns(line []byte) int {
	if !bytes.ContainsRune(line, '-') {
		return 0
	}
	for _, b := range line {
		if !util.IsSpace(b) && b != '-' && b != '|' && b != ':' {
			return 0
		}
	}
	cols := bytes.Split(line, []byte{'|'})
	if util.IsBlank(cols[0]) {
		cols = cols[1:]
	}
	if len(cols) > 0 && util.IsBlank(cols[len(cols)-1]) {
		cols = cols[:len(cols)-1]
	}
	return len(cols)
}
