package core

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type MatchFile struct {
	Path      string
	Filename  string
	Extension string
	Contents  []byte
	Size      int64  // Add file size
	loaded    bool   // Track if contents are loaded
}

// Buffer pool for reading files to reduce allocations
var bufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 32*1024) // 32KB buffers
		return &b
	},
}

// NewMatchFile creates a MatchFile with lazy content loading
func NewMatchFile(path string) MatchFile {
	path = filepath.ToSlash(path)
	_, filename := filepath.Split(path)
	extension := filepath.Ext(path)
	
	// Get file size WITHOUT reading contents
	stat, err := os.Stat(path)
	var size int64
	if err == nil {
		size = stat.Size()
	}

	return MatchFile{
		Path:      path,
		Filename:  filename,
		Extension: extension,
		Contents:  nil,  // Don't load yet
		Size:      size,
		loaded:    false,
	}
}

// GetContents lazily loads file contents only when needed
// Uses buffered reading for better performance
func (m *MatchFile) GetContents() []byte {
	if !m.loaded {
		// Check max file size before reading
		maxSize := int64(*session.Options.MaximumFileSize * 1024)
		if m.Size > maxSize {
			m.Contents = []byte{}
			m.loaded = true
			return m.Contents
		}
		
		// Use buffered reading for better I/O performance
		file, err := os.Open(m.Path)
		if err != nil {
			m.Contents = []byte{}
			m.loaded = true
			return m.Contents
		}
		defer file.Close()
		
		// Pre-allocate slice to exact size to avoid reallocations
		m.Contents = make([]byte, m.Size)
		
		reader := bufio.NewReaderSize(file, 64*1024) // 64KB buffer
		_, err = io.ReadFull(reader, m.Contents)
		if err != nil && err != io.ErrUnexpectedEOF {
			m.Contents = []byte{}
		}
		
		m.loaded = true
	}
	return m.Contents
}

// Pre-compute blacklist checks for faster lookup
var (
	blacklistExtMap  map[string]bool
	blacklistPathMap map[string]bool
	blacklistOnce    sync.Once
)

func initBlacklists() {
	blacklistOnce.Do(func() {
		blacklistExtMap = make(map[string]bool, len(session.Config.BlacklistedExtensions))
		for _, ext := range session.Config.BlacklistedExtensions {
			blacklistExtMap[strings.ToLower(ext)] = true
		}
		
		blacklistPathMap = make(map[string]bool, len(session.Config.BlacklistedPaths))
		for _, path := range session.Config.BlacklistedPaths {
			normalized := strings.Replace(path, "{sep}", string(os.PathSeparator), -1)
			blacklistPathMap[normalized] = true
		}
	})
}

func IsSkippableFile(path string) bool {
	initBlacklists()
	
	extension := strings.ToLower(filepath.Ext(path))

	// Fast map lookup instead of linear search
	if blacklistExtMap[extension] {
		return true
	}

	// Check path indicators - still need contains check but fewer iterations
	for indicator := range blacklistPathMap {
		if strings.Contains(path, indicator) {
			return true
		}
	}

	return false
}

func (match MatchFile) CanCheckEntropy() bool {
	if match.Filename == "id_rsa" {
		return false
	}

	for _, skippableExt := range session.Config.BlacklistedEntropyExtensions {
		if match.Extension == skippableExt {
			return false
		}
	}

	return true
}

// Optimized file walking with early filtering and parallel processing potential
func GetMatchingFiles(dir string) []MatchFile {
	fileList := make([]MatchFile, 0, 1000) // Pre-allocate with reasonable capacity
	maxFileSize := *session.Options.MaximumFileSize * 1024
	
	initBlacklists() // Ensure blacklists are ready

	filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		// Fast rejection checks first (cheapest to most expensive)
		if err != nil {
			return nil
		}
		if f.IsDir() {
			return nil
		}
		if uint(f.Size()) > maxFileSize {
			return nil
		}
		if IsSkippableFile(path) {
			return nil
		}
		
		fileList = append(fileList, NewMatchFile(path))
		return nil
	})

	return fileList
}
