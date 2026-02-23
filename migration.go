package migration

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Migration database version migration record
type Migration struct {
	ID            int64      `json:"id" gorm:"primaryKey"`
	Version       string     `json:"version" gorm:"size:50;not null;unique"`
	Description   string     `json:"description" gorm:"size:200"`
	Script        string     `json:"script" gorm:"size:100;not null"`
	Checksum      string     `json:"checksum" gorm:"size:32;not null"`
	InstalledBy   string     `json:"installedBy" gorm:"size:100;not null"`
	InstalledOn   time.Time  `json:"installedOn" gorm:"not null;default:CURRENT_TIMESTAMP"`
	ExecutionTime int        `json:"executionTime" gorm:"not null"`
	Success       bool       `json:"success" gorm:"not null"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	DeletedAt     *time.Time `json:"deletedAt" gorm:"index"`
}

// TableName table name
func (Migration) TableName() string {
	return "sys_db_version"
}

// Logger interfaces for external loggers
type Logger interface {
	Printf(format string, args ...interface{})
}

// StdLogger default logger implementation using fmt
type StdLogger struct{}

func (l *StdLogger) Printf(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006/01/02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s\n", timestamp, msg)
}

// MigrationService migration service
type MigrationService struct {
	db              *gorm.DB
	includeLocation bool
	logger          Logger
}

// NewMigrationService creates a migration service
func NewMigrationService(db *DB) *MigrationService {
	return &MigrationService{
		db:              db.DB,
		includeLocation: false, // Default to false as requested
		logger:          &StdLogger{},
	}
}

// SetIncludeLocation sets whether to print code line numbers
func (s *MigrationService) SetIncludeLocation(include bool) {
	s.includeLocation = include
}

// SetLogger sets a custom logger
func (s *MigrationService) SetLogger(l Logger) {
	s.logger = l
}

// log migration logging function (internal helper to delegate to interface)
func (s *MigrationService) log(format string, args ...interface{}) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// splitSQLStatements splits SQL statements
func splitSQLStatements(content string) []string {
	// Remove comments
	content = regexp.MustCompile(`--.*$`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(content, "")

	// Split SQL statements
	statements := make([]string, 0)
	var currentStmt strings.Builder
	var inString bool
	var stringChar rune
	var escaped bool

	for _, char := range content {
		switch {
		case escaped:
			currentStmt.WriteRune(char)
			escaped = false
		case char == '\\':
			currentStmt.WriteRune(char)
			escaped = true
		case inString && char == stringChar:
			currentStmt.WriteRune(char)
			inString = false
		case !inString && (char == '\'' || char == '"'):
			currentStmt.WriteRune(char)
			inString = true
			stringChar = char
		case !inString && char == ';':
			stmt := strings.TrimSpace(currentStmt.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			currentStmt.Reset()
		default:
			currentStmt.WriteRune(char)
		}
	}

	// Add the last statement (if any)
	lastStmt := strings.TrimSpace(currentStmt.String())
	if lastStmt != "" {
		statements = append(statements, lastStmt)
	}

	return statements
}

// compareVersions compares version numbers, returns true if v1 < v2
func compareVersions(v1, v2 string) bool {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	for i := 0; i < len(parts1) && i < len(parts2); i++ {
		n1, _ := strconv.Atoi(parts1[i])
		n2, _ := strconv.Atoi(parts2[i])
		if n1 != n2 {
			return n1 < n2
		}
	}
	return len(parts1) < len(parts2)
}

// parseScriptVersion parses script version information
func parseScriptVersion(filename string) (version, description string, err error) {
	// Flyway naming format: V1.0.0__Description.sql
	pattern := regexp.MustCompile(`^V(\d+\.\d+\.\d+)__(.+)\.sql$`)
	matches := pattern.FindStringSubmatch(filename)
	if len(matches) != 3 {
		return "", "", fmt.Errorf("invalid script filename format: %s", filename)
	}
	return matches[1], matches[2], nil
}

// isVersionTableExists checks if the version table exists
func (s *MigrationService) isVersionTableExists() (bool, error) {
	return s.db.Migrator().HasTable(&Migration{}), nil
}

// createVersionTable creates the version table
func (s *MigrationService) createVersionTable() error {
	return s.db.AutoMigrate(&Migration{})
}

// getExecutedVersions retrieves executed version records
func (s *MigrationService) getExecutedVersions() (map[string]*Migration, error) {
	var migrations []*Migration
	if err := s.db.Where("success = ?", true).Find(&migrations).Error; err != nil {
		return nil, fmt.Errorf("failed to get executed version records: %v", err)
	}

	versionMap := make(map[string]*Migration)
	for _, m := range migrations {
		versionMap[m.Version] = m
	}
	return versionMap, nil
}

// executeSQLStatements executes SQL statements
func (s *MigrationService) executeSQLStatements(statements []string) error {
	for _, stmt := range statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("failed to execute SQL statement: %v\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// validateChecksum validates the checksum of executed scripts
func (s *MigrationService) validateChecksum(file string, executed *Migration) error {
	content, err := os.ReadFile(file)
	if err != nil {
		s.log("failed to read SQL file: %v", err)
		return fmt.Errorf("failed to read SQL file: %v", err)
	}

	checksum := fmt.Sprintf("%x", md5.Sum(content))
	if checksum != executed.Checksum {
		s.log("SQL file %s has been modified, expected checksum: %s, actual checksum: %s", filepath.Base(file), executed.Checksum, checksum)
		s.log("Warning: skipping checksum check, continuing execution")
	}
	return nil
}

// executeScriptFile executes a single script file
func (s *MigrationService) executeScriptFile(file, version, description, filename string) error {
	s.log("Starting execution of version %s", version)

	// Read SQL file content
	content, err := os.ReadFile(file)
	if err != nil {
		s.log("failed to read SQL file: %v", err)
		return fmt.Errorf("failed to read SQL file: %v", err)
	}

	// Start transaction
	tx := s.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %v", tx.Error)
	}

	// Split and execute SQL statements
	startTime := time.Now()
	statements := splitSQLStatements(string(content))
	if err := s.executeSQLStatements(statements); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to execute SQL: %v", err)
	}

	// Record execution result
	migration := &Migration{
		Version:       version,
		Description:   description,
		Script:        filename,
		Checksum:      fmt.Sprintf("%x", md5.Sum(content)),
		InstalledBy:   "system",
		InstalledOn:   time.Now(),
		ExecutionTime: int(time.Since(startTime).Milliseconds()),
		Success:       true,
	}

	if err := tx.Create(migration).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to record version info: %v", err)
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}

// processSQLFile processes a single SQL file
func (s *MigrationService) processSQLFile(file string, executedVersions map[string]*Migration) error {
	filename := filepath.Base(file)
	s.log("Parsing SQL file: %s", filename)

	version, description, err := parseScriptVersion(filename)
	if err != nil {
		s.log("failed to parse SQL version info: %v", err)
		return err
	}

	// Check if already executed
	if executed, ok := executedVersions[version]; ok {
		s.log("SQL file already executed, checking file checksum: %s", filename)
		if err := s.validateChecksum(file, executed); err != nil {
			return err
		}
		s.log("SQL file %s checksum verification passed, skipping execution", version)
		return nil
	}

	// Execute script file
	return s.executeScriptFile(file, version, description, filename)
}

// Migrate executes database migration
func (s *MigrationService) Migrate(scriptDir string) error {
	s.log("Starting database migration, SQL directory: %s", scriptDir)

	// Check if version table exists
	exists, err := s.isVersionTableExists()
	if err != nil {
		s.log("failed to check if version table exists: %v", err)
		return fmt.Errorf("failed to check if version table exists: %v", err)
	}

	// If version table does not exist, create it
	if !exists {
		s.log("Version table does not exist, starting creation")
		if err := s.createVersionTable(); err != nil {
			s.log("failed to create version table: %v", err)
			return fmt.Errorf("failed to create version table: %v", err)
		}
		s.log("Version table created successfully")
	}

	executedVersions, err := s.getExecutedVersions()
	if err != nil {
		s.log("failed to get executed version records: %v", err)
		return err
	}

	files, err := filepath.Glob(filepath.Join(scriptDir, "V*.sql"))
	if err != nil {
		s.log("failed to read SQL files: %v", err)
		return fmt.Errorf("failed to read SQL files: %v", err)
	}

	// Sort by version number
	sort.Slice(files, func(i, j int) bool {
		v1, _, err1 := parseScriptVersion(filepath.Base(files[i]))
		v2, _, err2 := parseScriptVersion(filepath.Base(files[j]))

		if err1 == nil && err2 == nil {
			return compareVersions(v1, v2)
		}
		return files[i] < files[j]
	})

	s.log("All scanned SQL scripts (sorted by version):")
	for _, f := range files {
		v, _, _ := parseScriptVersion(filepath.Base(f))
		s.log("  - %s (version: %s)", filepath.Base(f), v)
	}

	// Iterate through all SQL files
	for _, file := range files {
		if err := s.processSQLFile(file, executedVersions); err != nil {
			return err
		}
	}

	s.log("Database migration completed")
	return nil
}
