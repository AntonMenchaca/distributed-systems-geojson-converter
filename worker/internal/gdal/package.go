package gdal

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Package turns Convert's output at path into the single file that is uploaded, and returns
// its path. A GeoPackage is already one file. A shapefile directory becomes Name.shp.zip with
// its files at the zip root; a FileGDB directory becomes Name.gdb.zip holding Name.gdb/. Both
// layouts are the ones GDAL opens directly.
func Package(path, format string) (string, error) {
	f, ok := formats[format]
	if !ok {
		return "", fmt.Errorf("gdal: unknown format %q", format)
	}
	if f.out == f.packaged {
		return path, nil
	}
	base := strings.TrimSuffix(path, f.out)
	zipPath := base + f.packaged
	// The shapefile's files sit at the zip root; the FileGDB keeps its .gdb directory.
	root := path
	if format == "fgdb" {
		root = filepath.Dir(path)
	}
	if err := zipDir(zipPath, path, root); err != nil {
		return "", fmt.Errorf("gdal: package %s: %w", format, err)
	}
	return zipPath, nil
}

// zipDir writes every file under dir into zipPath, named relative to root.
func zipDir(zipPath, dir, root string) (err error) {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(out)
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if walkErr != nil {
		return walkErr
	}
	return zw.Close()
}
