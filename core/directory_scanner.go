package core

import (
	"os"
	"path/filepath"
	"strings"
)

// DirectoryScanner handles scanning directories for secrets
type DirectoryScanner struct {
	log *Logger
}

// NewDirectoryScanner creates a new directory scanner
func NewDirectoryScanner(log *Logger) *DirectoryScanner {
	return &DirectoryScanner{log: log}
}

// ScanDirectory recursively scans a directory and returns all matching files
func (ds *DirectoryScanner) ScanDirectory(dir string) []MatchFile {
	var files []MatchFile
	maxFileSize := uint(256 * 1024) // 256KB default

	filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() || uint(f.Size()) > maxFileSize {
			return nil
		}

		// Skip blacklisted files
		if IsSkippableFile(path) {
			return nil
		}

		files = append(files, NewMatchFile(path))
		return nil
	})

	return files
}

// ScanDirectoryWithOptions scans a directory with custom options
func (ds *DirectoryScanner) ScanDirectoryWithOptions(dir string, maxFileSize uint, blacklistedExts []string, blacklistedPaths []string) []MatchFile {
	var files []MatchFile

	filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() || uint(f.Size()) > maxFileSize {
			return nil
		}

		// Check blacklisted extensions
		ext := strings.ToLower(filepath.Ext(path))
		for _, blackExt := range blacklistedExts {
			if ext == blackExt {
				return nil
			}
		}

		// Check blacklisted paths
		for _, blackPath := range blacklistedPaths {
			blackPath = strings.Replace(blackPath, "{sep}", string(os.PathSeparator), -1)
			if strings.Contains(path, blackPath) {
				return nil
			}
		}

		files = append(files, NewMatchFile(path))
		return nil
	})

	return files
}

// GetFileCount returns the total number of files in a directory
func (ds *DirectoryScanner) GetFileCount(dir string) int {
	count := 0
	filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() {
			return nil
		}
		count++
		return nil
	})
	return count
}

// GetDirectorySize returns the total size of a directory in bytes
func (ds *DirectoryScanner) GetDirectorySize(dir string) int64 {
	var size int64
	filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() {
			return nil
		}
		size += f.Size()
		return nil
	})
	return size
}
