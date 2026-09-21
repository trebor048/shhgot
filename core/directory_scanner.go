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
	// done is closed as soon as any worker reports an error. The producer
	// selects on it, so it can never block forever on a full items channel after
	// every worker has returned - which is exactly what the old producer did,
	// leaking a goroutine and hanging the walk.
	done := make(chan struct{})
	var wg sync.WaitGroup

	var walkErrMu sync.Mutex
	var walkErr error
	var closeOnce sync.Once
	// reportErr records the first error and stops the producer. A plain error
	// variable under a mutex replaces the old atomic.Value, which panics when
	// two workers store errors of different concrete types.
	reportErr := func(err error) {
		walkErrMu.Lock()
		if walkErr == nil {
			walkErr = err
		}
		walkErrMu.Unlock()
		closeOnce.Do(func() { close(done) })
	}
	firstErr := func() error {
		walkErrMu.Lock()
		defer walkErrMu.Unlock()
		return walkErr
	}

	// Worker pool to process items
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range items {
				if err := walkFn(item.path, item.info, item.err); err != nil {
					reportErr(err)
					return
				}
			}
		}()
	}

	// Single goroutine to walk directory tree and feed workers
	go func() {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			// Check if any worker reported an error.
			if firstErr() != nil {
				return filepath.SkipDir
			}

			select {
			case items <- walkItem{path, info, err}:
				return nil
			case <-done:
				return filepath.SkipDir
			}
		})
		close(items)
	}()

	wg.Wait()

	if e := firstErr(); e != nil {
		return e
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
