package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// rootFS keeps resolved targets inside root; os.DirFS alone allows an in-root symlink to reach outside
type rootFS struct {
	root string
	fsys fs.FS
}

func newRootFS(root string) (*rootFS, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("get absolute root directory: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root directory: %w", err)
	}

	return &rootFS{root: resolvedRoot, fsys: os.DirFS(resolvedRoot)}, nil
}

func (r *rootFS) Open(name string) (fs.File, error) {
	if err := r.validate("open", name); err != nil {
		return nil, err
	}
	return r.fsys.Open(name)
}

func (r *rootFS) Stat(name string) (fs.FileInfo, error) {
	if err := r.validate("stat", name); err != nil {
		return nil, err
	}
	return fs.Stat(r.fsys, name)
}

func (r *rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := r.validate("readdir", name); err != nil {
		return nil, err
	}
	return fs.ReadDir(r.fsys, name)
}

func (r *rootFS) validate(op, name string) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(r.root, filepath.FromSlash(name)))
	if err != nil {
		return &fs.PathError{Op: op, Path: name, Err: err}
	}

	rel, err := filepath.Rel(r.root, resolved)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	}

	// the path can change after evaluation and before the operation. Closing that TOCTOU window
	// requires descriptor-relative traversal, which would also reject compatible absolute symlinks.
	return nil
}
