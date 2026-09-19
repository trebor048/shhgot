package core

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Type string // "sqlite" or "postgres"
	URL  string // Connection string
}

// InitDB initializes database connection
func InitDB(config DatabaseConfig) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	switch config.Type {
	case "postgres":
		log.Printf("[DB] Connecting to PostgreSQL: %s", config.URL)
		db, err = gorm.Open(postgres.Open(config.URL), &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
		}

	case "sqlite", "":
		// Default to SQLite
		dbPath := config.URL
		if dbPath == "" {
			dbPath = "shhgit.db"
		}
		log.Printf("[DB] Using SQLite: %s", dbPath)
		db, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SQLite: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported database type: %s", config.Type)
	}

	// Initialize schema
	if err := InitializeDatabase(db); err != nil {
		return nil, fmt.Errorf("failed to initialize database schema: %w", err)
	}

	// Create indexes
	if err := CreateIndexes(db); err != nil {
		log.Printf("[DB] Warning: failed to create indexes: %v", err)
	}

	log.Printf("[DB] Database initialized successfully")

	return db, nil
}

// MigrateMatches migrates matches from in-memory to database
func MigrateMatches(db *gorm.DB, matches interface{}) error {
	log.Printf("[DB] Migrating matches to database")
	// This is a placeholder - actual migration depends on match struct
	// In production, iterate through matches and insert into MatchModel
	return nil
}

// GetMatchesPaginated retrieves paginated matches from database
func GetMatchesPaginated(db *gorm.DB, page, limit int, filters map[string]interface{}) ([]*MatchModel, int64, error) {
	var matches []*MatchModel
	var total int64

	query := db

	// Apply filters
	if source, ok := filters["source"].(string); ok && source != "" {
		query = query.Where("source = ?", source)
	}

	if signature, ok := filters["signature"].(string); ok && signature != "" {
		query = query.Where("signature = ?", signature)
	}

	if priority, ok := filters["priority"].(int); ok && priority >= 0 {
		query = query.Where("priority = ?", priority)
	}

	if archive, ok := filters["archived"].(bool); ok {
		query = query.Where("archived = ?", archive)
	}

	// Count total
	if err := query.Model(&MatchModel{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Calculate offset
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}

	// Fetch paginated results
	if err := query.Offset(offset).Limit(limit).
		Order("timestamp DESC").
		Find(&matches).Error; err != nil {
		return nil, 0, err
	}

	return matches, total, nil
}

// SearchMatches searches matches by query
func SearchMatches(db *gorm.DB, query string, limit int) ([]*MatchModel, error) {
	var matches []*MatchModel

	if err := db.Where("file LIKE ? OR url LIKE ? OR signature LIKE ?",
		"%"+query+"%", "%"+query+"%", "%"+query+"%").
		Limit(limit).
		Find(&matches).Error; err != nil {
		return nil, err
	}

	return matches, nil
}

// DeleteMatch soft-deletes a match (audit trail)
func DeleteMatch(db *gorm.DB, matchID string) error {
	return db.Where("id = ?", matchID).Delete(&MatchModel{}).Error
}

// ArchiveOldMatches archives matches older than days
func ArchiveOldMatches(db *gorm.DB, days int) error {
	thirtyDaysAgo := time.Now().AddDate(0, 0, -days)
	return db.Where("created_at < ?", thirtyDaysAgo).
		Update("archived", true).Error
}

// LogAuditEvent logs an audit trail event
func LogAuditEvent(db *gorm.DB, userID, action, resource, oldValue, newValue, ipAddress string) error {
	auditLog := &AuditLog{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		UserID:    userID,
		Action:    action,
		Resource:  resource,
		OldValue:  oldValue,
		NewValue:  newValue,
		IPAddress: ipAddress,
		Timestamp: time.Now(),
	}
	return db.Create(auditLog).Error
}

// GetAuditLog retrieves audit logs
func GetAuditLog(db *gorm.DB, userID string, limit int) ([]*AuditLog, error) {
	var logs []*AuditLog

	query := db.Order("timestamp DESC").Limit(limit)
	if userID != "" {
		query = query.Where("user_id = ?", userID)
	}

	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}

	return logs, nil
}
