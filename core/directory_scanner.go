package core

import (
	"os"
	"path/filepath"
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
