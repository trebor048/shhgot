package core

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type CleanupManager struct {
	sync.Mutex
	tempDir              string
	maxDiskUsageMB       uint64
	cleanupThresholdMB   uint64
	lastCleanupTime      time.Time
	cleanupIntervalSecs  int
	processedDirectories map[string]time.Time
	// activeDirectories counts live clone/scan checkouts per path. Eviction
	// must skip them: temp dirs are deterministic per URL hash, so a stale
	// "processed" entry from a previous pass would otherwise let cleanup
	// RemoveAll a directory a fresh scan is standing in.
	activeDirectories map[string]int
	// log is the session logger; nil disables cleanup logging. It is injected
	// (not read from the package-global session) so the manager stays usable
	// from tests and never triggers global session init on a logging call.
	log *Logger
}

// NewCleanupManager creates a new cleanup manager
func NewCleanupManager(tempDir string, maxDiskUsageMB uint64, log *Logger) *CleanupManager {
	return &CleanupManager{
		tempDir:              tempDir,
		maxDiskUsageMB:       maxDiskUsageMB,
		cleanupThresholdMB:   maxDiskUsageMB / 2, // Start cleanup at 50% capacity
		lastCleanupTime:      time.Now(),
		cleanupIntervalSecs:  30, // Check every 30 seconds
		processedDirectories: make(map[string]time.Time),
		activeDirectories:    make(map[string]int),
		log:                  log,
	}
}

// BeginDir registers a live checkout so eviction skips it. Callers must pair
// every BeginDir with EndDir (defer it); the count is a refcount because the
// same deterministic path can be entered more than once.
func (cm *CleanupManager) BeginDir(dir string) {
	cm.Lock()
	defer cm.Unlock()
	cm.activeDirectories[dir]++
}

// EndDir unregisters a live checkout previously registered with BeginDir.
func (cm *CleanupManager) EndDir(dir string) {
	cm.Lock()
	defer cm.Unlock()
	if cm.activeDirectories[dir] <= 1 {
		delete(cm.activeDirectories, dir)
		return
	}
	cm.activeDirectories[dir]--
}

// MarkProcessed marks a directory as processed for cleanup tracking. A
// directory the caller already removed is dropped instead of retained, so the
// map cannot grow without bound with entries that will never be pruned.
func (cm *CleanupManager) MarkProcessed(dir string) {
	if _, err := os.Stat(dir); err != nil {
		cm.Lock()
		delete(cm.processedDirectories, dir)
		cm.Unlock()
		return
	}
	cm.Lock()
	defer cm.Unlock()
	cm.processedDirectories[dir] = time.Now()
}

// debug logs through the injected logger, dropping the message when none is
// set (tests).
func (cm *CleanupManager) debug(format string, args ...interface{}) {
	if cm != nil && cm.log != nil {
		cm.log.Debug(format, args...)
	}
}

// warn logs through the injected logger, dropping the message when none is
// set (tests).
func (cm *CleanupManager) warn(format string, args ...interface{}) {
	if cm != nil && cm.log != nil {
		cm.log.Warn(format, args...)
	}
}

// GetDiskUsage calculates total disk usage of temp directory in MB
func (cm *CleanupManager) GetDiskUsage() (uint64, error) {
	var totalSize int64
	err := filepath.Walk(cm.tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return uint64(totalSize / (1024 * 1024)), err
}

// diskUsageBytes returns the temp directory's total size in bytes. The eviction
// loop works in bytes because the MB figure floors each sub-megabyte directory
// to 0, which made the target unreachable and over-deleted the whole tree.
func (cm *CleanupManager) diskUsageBytes() uint64 {
	var total int64
	filepath.Walk(cm.tempDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return uint64(total)
}

// CleanupIfNeeded checks disk usage and cleans up old directories if needed
func (cm *CleanupManager) CleanupIfNeeded() error {
	cm.Lock()
	// Only check cleanup every N seconds to avoid excessive I/O
	if time.Since(cm.lastCleanupTime).Seconds() < float64(cm.cleanupIntervalSecs) {
		cm.Unlock()
		return nil
	}
	cm.lastCleanupTime = time.Now()
	cm.Unlock()

	// Everything below is slow I/O - a full recursive walk, then more walks and
	// removals. It runs without the lock so a scanner worker's MarkProcessed is
	// not stuck behind it for the length of a disk sweep.
	usage, err := cm.GetDiskUsage()
	if err != nil {
		return err
	}

	if usage > cm.maxDiskUsageMB {
		cm.warn("Disk usage (%dMB) exceeds maximum (%dMB). Starting cleanup...", usage, cm.maxDiskUsageMB)
		return cm.cleanupOldDirectories()
	}

	if usage > cm.cleanupThresholdMB {
		cm.debug("Disk usage (%dMB) approaching threshold (%dMB). Cleaning up old directories...", usage, cm.cleanupThresholdMB)
		return cm.cleanupOldDirectories()
	}

	return nil
}

// cleanupOldDirectories removes least recently used directories
func (cm *CleanupManager) cleanupOldDirectories() error {
	entries, err := os.ReadDir(cm.tempDir)
	if err != nil {
		return err
	}

	type dirInfo struct {
		path    string
		modTime time.Time
		size    int64
	}

	// Snapshot the processed map under the lock; the per-directory walks below are
	// slow and must not hold it.
	cm.Lock()
	processed := make(map[string]time.Time, len(cm.processedDirectories))
	for k, v := range cm.processedDirectories {
		processed[k] = v
	}
	cm.Unlock()

	var dirs []dirInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		fullPath := filepath.Join(cm.tempDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Get the most recent modification time in the directory
		modTime := info.ModTime()
		if processedTime, exists := processed[fullPath]; exists {
			modTime = processedTime
		}

		size := getDirSize(fullPath)
		dirs = append(dirs, dirInfo{
			path:    fullPath,
			modTime: modTime,
			size:    size,
		})
	}

	// Sort by modification time (oldest first)
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].modTime.Before(dirs[j].modTime)
	})

	// Remove the oldest COMPLETED directories until we're below the target. An
	// in-progress clone is never evicted, so a worker's scan cannot have its
	// directory pulled out from under it.
	targetBytes := (cm.cleanupThresholdMB / 2) * 1024 * 1024
	currentBytes := cm.diskUsageBytes()

	for _, dir := range dirs {
		if currentBytes <= targetBytes {
			break
		}
		cm.Lock()
		_, done := cm.processedDirectories[dir.path]
		_, live := cm.activeDirectories[dir.path]
		cm.Unlock()
		if !done || live {
			continue
		}

		err := os.RemoveAll(dir.path)
		if err == nil {
			cm.debug("Cleaned up directory: %s (freed %dMB)", dir.path, dir.size/(1024*1024))
			cm.Lock()
			delete(cm.processedDirectories, dir.path)
			cm.Unlock()
			if uint64(dir.size) > currentBytes {
				currentBytes = 0
			} else {
				currentBytes -= uint64(dir.size)
			}
		}
	}

	return nil
}

// getDirSize calculates the total size of a directory in bytes
func getDirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}
