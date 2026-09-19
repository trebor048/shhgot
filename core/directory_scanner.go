package core

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// DirectoryScanner handles scanning directories for secrets
type DirectoryScanner struct {
	log *Logger
}

// NewDirectoryScanner creates a new directory scanner
func NewDirectoryScanner(log *Logger) *DirectoryScanner {
	return &DirectoryScanner{log: log}
}

// FastWalk is an optimized parallel directory walker
func FastWalk(root string, walkFn func(path string, info os.FileInfo, err error) error, maxWorkers int) error {
	if maxWorkers <= 0 {
		maxWorkers = 4 // Default to 4 parallel workers
	}

	type walkItem struct {
		path string
		info os.FileInfo
		err  error
	}

	// Buffered channel for discovered items
	items := make(chan walkItem, 1000)
	var wg sync.WaitGroup
	var walkErr atomic.Value

	// Worker pool to process items
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range items {
				if err := walkFn(item.path, item.info, item.err); err != nil {
					walkErr.Store(err)
					return
				}
			}
		}()
	}

	// Single goroutine to walk directory tree and feed workers
	go func() {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			// Check if any worker reported error
			if e := walkErr.Load(); e != nil {
				return filepath.SkipDir
			}

			items <- walkItem{path, info, err}
			return nil
		})
		close(items)
	}()

	wg.Wait()

	if e := walkErr.Load(); e != nil {
		return e.(error)
	}
	return nil
}

// ScanDirectory recursively scans a directory and returns all matching files
// Now uses parallelization for faster scanning
func (ds *DirectoryScanner) ScanDirectory(dir string) []MatchFile {
	var files []MatchFile
	var mu sync.Mutex
	maxFileSize := uint(256 * 1024) // 256KB default

	FastWalk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() || uint(f.Size()) > maxFileSize {
			return nil
		}

		// Skip blacklisted files
		if IsSkippableFile(path) {
			return nil
		}

		mu.Lock()
		files = append(files, NewMatchFile(path))
		mu.Unlock()
		return nil
	}, 8) // Use 8 parallel workers

	return files
}

// ScanDirectoryWithOptions scans a directory with custom options
// Now uses fast map lookups and parallelization
func (ds *DirectoryScanner) ScanDirectoryWithOptions(dir string, maxFileSize uint, blacklistedExts []string, blacklistedPaths []string) []MatchFile {
	var files []MatchFile
	var mu sync.Mutex

	// Pre-build maps for faster lookups
	extMap := make(map[string]bool, len(blacklistedExts))
	for _, ext := range blacklistedExts {
		extMap[strings.ToLower(ext)] = true
	}

	pathIndicators := make([]string, len(blacklistedPaths))
	for i, path := range blacklistedPaths {
		pathIndicators[i] = strings.Replace(path, "{sep}", string(os.PathSeparator), -1)
	}

	FastWalk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() || uint(f.Size()) > maxFileSize {
			return nil
		}

		// Fast extension check with map
		ext := strings.ToLower(filepath.Ext(path))
		if extMap[ext] {
			return nil
		}

		// Check blacklisted paths
		for _, indicator := range pathIndicators {
			if strings.Contains(path, indicator) {
				return nil
			}
		}

		mu.Lock()
		files = append(files, NewMatchFile(path))
		mu.Unlock()
		return nil
	}, 8) // Use 8 parallel workers

	return files
}

// GetFileCount returns the total number of files in a directory
// Optimized with parallel counting
func (ds *DirectoryScanner) GetFileCount(dir string) int {
	var count int64

	FastWalk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() {
			return nil
		}
		atomic.AddInt64(&count, 1)
		return nil
	}, 4)

	return int(count)
}

// GetDirectorySize returns the total size of a directory in bytes
// Optimized with parallel size calculation
func (ds *DirectoryScanner) GetDirectorySize(dir string) int64 {
	var size int64

	FastWalk(dir, func(path string, f os.FileInfo, err error) error {
		if err != nil || f.IsDir() {
			return nil
		}
		atomic.AddInt64(&size, f.Size())
		return nil
	}, 4)

	return size
}

// ReadFileOptimized reads a file with buffered I/O for better performance
func ReadFileOptimized(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	// Pre-allocate buffer to exact size
	buf := make([]byte, stat.Size())
	reader := bufio.NewReaderSize(file, 64*1024) // 64KB buffer

	_, err = reader.Read(buf)
	if err != nil {
		return nil, err
	}

	return buf, nil
}
