package migration

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Migration 数据库版本迁移记录
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

// TableName 表名
func (Migration) TableName() string {
	return "sys_db_version"
}

// MigrationService 迁移服务
type MigrationService struct {
	db *gorm.DB
}

// NewMigrationService 创建迁移服务
func NewMigrationService(db *DB) *MigrationService {
	return &MigrationService{db: db.DB}
}

// migrationLog 迁移日志函数
func migrationLog(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006/01/02 15:04:05.000")
	_, file, line, _ := runtime.Caller(1)
	fileName := filepath.Base(file)
	fmt.Printf("%s %s:%d %s\n", timestamp, fileName, line, fmt.Sprintf(format, args...))
}

// splitSQLStatements 分割SQL语句
func splitSQLStatements(content string) []string {
	// 移除注释
	content = regexp.MustCompile(`--.*$`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(content, "")

	// 分割SQL语句
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

	// 添加最后一个语句（如果有）
	lastStmt := strings.TrimSpace(currentStmt.String())
	if lastStmt != "" {
		statements = append(statements, lastStmt)
	}

	return statements
}

// parseVersionParts 解析版本号各部分为整数数组
func parseVersionParts(version string) []int {
	parts := strings.Split(version, ".")
	result := make([]int, len(parts))
	for i, p := range parts {
		val, err := strconv.Atoi(p)
		if err != nil {
			result[i] = 0
		} else {
			result[i] = val
		}
	}
	return result
}

// compareVersions 比较两个版本号，返回 -1, 0, 1
// a < b 返回 -1, a == b 返回 0, a > b 返回 1
func compareVersions(a, b string) int {
	partsA := parseVersionParts(a)
	partsB := parseVersionParts(b)

	// 确保两个数组长度一致
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var valA, valB int
		if i < len(partsA) {
			valA = partsA[i]
		}
		if i < len(partsB) {
			valB = partsB[i]
		}

		if valA < valB {
			return -1
		}
		if valA > valB {
			return 1
		}
	}
	return 0
}

// extractVersionFromFile 从文件路径提取版本号
func extractVersionFromFile(file string) string {
	filename := filepath.Base(file)
	version, _, err := parseScriptVersion(filename)
	if err != nil {
		return ""
	}
	return version
}

// parseScriptVersion 解析脚本版本信息
func parseScriptVersion(filename string) (version, description string, err error) {
	// Flyway命名格式: V1.0.0__Description.sql
	pattern := regexp.MustCompile(`^V(\d+\.\d+\.\d+)__(.+)\.sql$`)
	matches := pattern.FindStringSubmatch(filename)
	if len(matches) != 3 {
		return "", "", fmt.Errorf("invalid script filename format: %s", filename)
	}
	return matches[1], matches[2], nil
}

// isVersionTableExists 检查版本表是否存在
func (s *MigrationService) isVersionTableExists() (bool, error) {
	return s.db.Migrator().HasTable(&Migration{}), nil
}

// createVersionTable 创建版本表
func (s *MigrationService) createVersionTable() error {
	return s.db.AutoMigrate(&Migration{})
}

// getExecutedVersions 获取已执行的版本记录
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

// executeSQLStatements 执行SQL语句
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

// validateChecksum 验证已执行脚本的校验和
func (s *MigrationService) validateChecksum(file string, executed *Migration) error {
	content, err := os.ReadFile(file)
	if err != nil {
		migrationLog("failed to read SQL file: %v", err)
		return fmt.Errorf("failed to read SQL file: %v", err)
	}

	checksum := fmt.Sprintf("%x", md5.Sum(content))
	if checksum != executed.Checksum {
		migrationLog("SQL file %s has been modified, expected checksum: %s, actual checksum: %s", filepath.Base(file), executed.Checksum, checksum)
		migrationLog("Warning: skipping checksum check, continuing execution")
	}
	return nil
}

// executeScriptFile 执行单个脚本文件
func (s *MigrationService) executeScriptFile(file, version, description, filename string) error {
	migrationLog("Starting execution of version %s", version)

	// 读取SQL文件内容
	content, err := os.ReadFile(file)
	if err != nil {
		migrationLog("failed to read SQL file: %v", err)
		return fmt.Errorf("failed to read SQL file: %v", err)
	}

	// 开始事务
	tx := s.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %v", tx.Error)
	}

	// 分割并执行SQL语句
	startTime := time.Now()
	statements := splitSQLStatements(string(content))
	if err := s.executeSQLStatements(statements); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to execute SQL: %v", err)
	}

	// 记录执行结果
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

	// 提交事务
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}

// processSQLFile 处理单个SQL文件
func (s *MigrationService) processSQLFile(file string, executedVersions map[string]*Migration) error {
	filename := filepath.Base(file)
	migrationLog("Parsing SQL file: %s", filename)

	version, description, err := parseScriptVersion(filename)
	if err != nil {
		migrationLog("failed to parse SQL version info: %v", err)
		return err
	}

	// 检查是否已经执行过
	if executed, ok := executedVersions[version]; ok {
		migrationLog("SQL file already executed, checking file checksum: %s", filename)
		if err := s.validateChecksum(file, executed); err != nil {
			return err
		}
		migrationLog("SQL file %s checksum verification passed, skipping execution", version)
		return nil
	}

	// 执行脚本文件
	return s.executeScriptFile(file, version, description, filename)
}

// Migrate 执行数据库迁移
func (s *MigrationService) Migrate(scriptDir string) error {
	migrationLog("Starting database migration, SQL directory: %s", scriptDir)

	// 检查版本表是否存在
	exists, err := s.isVersionTableExists()
	if err != nil {
		migrationLog("failed to check if version table exists: %v", err)
		return fmt.Errorf("failed to check if version table exists: %v", err)
	}

	// 如果版本表不存在，创建它
	if !exists {
		migrationLog("Version table does not exist, starting creation")
		if err := s.createVersionTable(); err != nil {
			migrationLog("failed to create version table: %v", err)
			return fmt.Errorf("failed to create version table: %v", err)
		}
		migrationLog("Version table created successfully")
	}

	executedVersions, err := s.getExecutedVersions()
	if err != nil {
		migrationLog("failed to get executed version records: %v", err)
		return err
	}
	migrationLog("Versions already recorded in database (total %d):", len(executedVersions))
	for v := range executedVersions {
		migrationLog("  - %s", v)
	}

	files, err := filepath.Glob(filepath.Join(scriptDir, "V*.sql"))
	if err != nil {
		migrationLog("failed to read SQL files: %v", err)
		return fmt.Errorf("failed to read SQL files: %v", err)
	}

	// 按文件名排序
	// 按版本号语义化排序
	sort.Slice(files, func(i, j int) bool {
		verI := extractVersionFromFile(files[i])
		verJ := extractVersionFromFile(files[j])
		return compareVersions(verI, verJ) < 0
	})

	migrationLog("All scanned SQL scripts (sorted by version):")
	for _, f := range files {
		migrationLog("  - %s (version: %s)", filepath.Base(f), extractVersionFromFile(f))
	}

	// 遍历所有SQL文件
	for _, file := range files {
		if err := s.processSQLFile(file, executedVersions); err != nil {
			return err
		}
	}

	migrationLog("Database migration completed")
	return nil
}
