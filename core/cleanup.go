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
}

// NewCleanupManager creates a new cleanup manager
func NewCleanupManager(tempDir string, maxDiskUsageMB uint64) *CleanupManager {
	return &CleanupManager{
		tempDir:              tempDir,
		maxDiskUsageMB:       maxDiskUsageMB,
		cleanupThresholdMB:   maxDiskUsageMB / 2, // Start cleanup at 50% capacity
		lastCleanupTime:      time.Now(),
		cleanupIntervalSecs:  30, // Check every 30 seconds
		processedDirectories: make(map[string]time.Time),
	}
}

// MarkProcessed marks a directory as processed for cleanup tracking
func (cm *CleanupManager) MarkProcessed(dir string) {
	cm.Lock()
	defer cm.Unlock()
	cm.processedDirectories[dir] = time.Now()
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

// CleanupIfNeeded checks disk usage and cleans up old directories if needed
func (cm *CleanupManager) CleanupIfNeeded() error {
	cm.Lock()
	defer cm.Unlock()

	// Only check cleanup every N seconds to avoid excessive I/O
	if time.Since(cm.lastCleanupTime).Seconds() < float64(cm.cleanupIntervalSecs) {
		return nil
	}

	cm.lastCleanupTime = time.Now()

	usage, err := cm.GetDiskUsage()
	if err != nil {
		return err
	}

	if usage > cm.maxDiskUsageMB {
		GetSession().Log.Warn("Disk usage (%dMB) exceeds maximum (%dMB). Starting cleanup...", usage, cm.maxDiskUsageMB)
		return cm.cleanupOldDirectories()
	}

	if usage > cm.cleanupThresholdMB {
		GetSession().Log.Debug("Disk usage (%dMB) approaching threshold (%dMB). Cleaning up old directories...", usage, cm.cleanupThresholdMB)
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
		if processedTime, exists := cm.processedDirectories[fullPath]; exists {
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

	// Remove oldest directories until we're below threshold
	targetUsage := cm.cleanupThresholdMB / 2
	currentUsage, _ := cm.GetDiskUsage()

	for _, dir := range dirs {
		if currentUsage <= targetUsage {
			break
		}

		err := os.RemoveAll(dir.path)
		if err == nil {
			GetSession().Log.Debug("Cleaned up directory: %s (freed %dMB)", dir.path, dir.size/(1024*1024))
			delete(cm.processedDirectories, dir.path)
			currentUsage -= uint64(dir.size / (1024 * 1024))
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
