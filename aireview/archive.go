package aireview

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ArchiveJob zips the job artifacts (job.json, logs.txt, result.json,
// result.md, evidence/*) into root/archive/<id>.zip. Non-destructive.
func ArchiveJob(root, id string) (string, error) {
	src := filepath.Join(root, "jobs", id)
	arcDir := filepath.Join(root, "archive")
	if err := os.MkdirAll(arcDir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(arcDir, id+".zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("archive failed: %w", err)
	}
	return zipPath, nil
}
