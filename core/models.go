package core

import (
	"time"

	"gorm.io/gorm"
)

// Match represents a secret match stored in database
type MatchModel struct {
	ID        string    `gorm:"primaryKey"`
	Timestamp time.Time `gorm:"index:idx_timestamp"`
	Source    string    `gorm:"index:idx_source"`
	URL       string
	File      string `gorm:"index:idx_file"`
	Signature string `gorm:"index:idx_signature"`
	Priority  int    `gorm:"index:idx_priority"`
	Matches   string `gorm:"type:text"` // JSON-encoded []string
	Secret    string `gorm:"type:text"`
	Line      int
	Stars     int
	Color     string
	Content   string `gorm:"type:text"`
	Archived  bool   `gorm:"default:false;index:idx_archived"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// TokenModel represents a validated API token
type TokenModel struct {
	ID        string `gorm:"primaryKey"`
	UserID    string `gorm:"index"`
	Token     string `gorm:"index"`
	Valid     bool
	Provider  string
	Timestamp time.Time `gorm:"index"`
	CreatedAt time.Time
}

// AuditLog represents an audit trail entry
type AuditLog struct {
	ID        string `gorm:"primaryKey"`
	UserID    string `gorm:"index"`
	Action    string `gorm:"index"` // VIEW, DELETE, EXPORT, etc.
	Resource  string
	OldValue  string `gorm:"type:text"`
	NewValue  string `gorm:"type:text"`
	IPAddress string
	Timestamp time.Time `gorm:"index"`
	CreatedAt time.Time
}

// APIKey represents an API key for authentication
type APIKey struct {
	ID        string `gorm:"primaryKey"`
	UserID    string `gorm:"index"`
	Key       string `gorm:"index;uniqueIndex"`
	Name      string
	Scopes    string // Comma-separated: read:matches, write:matches, delete:matches
	LastUsed  *time.Time
	ExpiresAt *time.Time
	Revoked   bool `gorm:"default:false;index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InitializeDatabase initializes database schema
func InitializeDatabase(db *gorm.DB) error {
	// Auto-migrate all models
	return db.AutoMigrate(
		&MatchModel{},
		&TokenModel{},
		&AuditLog{},
		&APIKey{},
	)
}

// CreateIndexes creates additional database indexes for performance
func CreateIndexes(db *gorm.DB) error {
	// Composite indexes for common queries
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_source_priority ON match_models(source, priority)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_signature_timestamp ON match_models(signature, timestamp DESC)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_user_action ON audit_logs(user_id, action, timestamp DESC)`).Error; err != nil {
		return err
	}

	return nil
}
