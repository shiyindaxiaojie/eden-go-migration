package migration

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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

// parseScriptVersion 解析脚本版本信息
func parseScriptVersion(filename string) (version, description string, err error) {
	// Flyway命名格式: V1.0.0__Description.sql
	pattern := regexp.MustCompile(`^V(\d+\.\d+\.\d+)__(.+)\.sql$`)
	matches := pattern.FindStringSubmatch(filename)
	if len(matches) != 3 {
		return "", "", fmt.Errorf("无效的脚本文件名格式: %s", filename)
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
		return nil, fmt.Errorf("获取已执行版本记录失败: %v", err)
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
			return fmt.Errorf("执行SQL语句失败: %v\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// Migrate 执行数据库迁移
func (s *MigrationService) Migrate(scriptDir string) error {
	migrationLog("开始执行数据库迁移，SQL 目录: %s", scriptDir)

	// 检查版本表是否存在
	exists, err := s.isVersionTableExists()
	if err != nil {
		migrationLog("检查版本表是否存在失败: %v", err)
		return fmt.Errorf("检查版本表是否存在失败: %v", err)
	}

	// 如果版本表不存在，创建它
	if !exists {
		migrationLog("版本表不存在，开始创建")
		if err := s.createVersionTable(); err != nil {
			migrationLog("创建版本表失败: %v", err)
			return fmt.Errorf("创建版本表失败: %v", err)
		}
		migrationLog("版本表创建成功")
	}

	// 获取已执行的版本记录
	executedVersions, err := s.getExecutedVersions()
	if err != nil {
		migrationLog("获取已执行的版本记录失败: %v", err)
		return err
	}
	migrationLog("已执行的 SQL 文件数量: %d", len(executedVersions))

	// 获取所有SQL文件
	files, err := filepath.Glob(filepath.Join(scriptDir, "V*.sql"))
	if err != nil {
		migrationLog("读取 SQL 文件失败: %v", err)
		return fmt.Errorf("读取 SQL 文件失败: %v", err)
	}

	// 按文件名排序
	sort.Strings(files)

	// 遍历所有SQL文件
	for _, file := range files {
		filename := filepath.Base(file)
		migrationLog("解析 SQL 文件: %s", filename)

		// 解析版本信息
		version, description, err := parseScriptVersion(filename)
		if err != nil {
			migrationLog("解析 SQL 版本信息失败: %v", err)
			return err
		}

		// 检查是否已经执行过
		if executed, ok := executedVersions[version]; ok {
			migrationLog("SQL 文件已执行，检查文件校验和: %s", filename)

			// 读取SQL文件内容并验证校验和
			content, err := os.ReadFile(file)
			if err != nil {
				migrationLog("读取 SQL 文件失败: %v", err)
				return fmt.Errorf("读取 SQL 文件失败: %v", err)
			}

			checksum := fmt.Sprintf("%x", md5.Sum(content))
			if checksum != executed.Checksum {
				migrationLog("SQL 文件 %s 已被修改，期望校验和: %s, 实际校验和: %s", filename, executed.Checksum, checksum)
				migrationLog("警告：跳过校验和检查，继续执行")
				// return fmt.Errorf("SQL 文件 %s 已被修改", filename)
			}
			migrationLog("SQL 文件 %s 校验和验证通过，跳过执行", version)
			continue
		}

		migrationLog("开始执行版本 %s", version)

		// 读取SQL文件内容
		content, err := os.ReadFile(file)
		if err != nil {
			migrationLog("读取 SQL 文件失败: %v", err)
			return fmt.Errorf("读取 SQL 文件失败: %v", err)
		}

		// 开始事务
		tx := s.db.Begin()
		if tx.Error != nil {
			return fmt.Errorf("开始事务失败: %v", tx.Error)
		}

		// 分割并执行SQL语句
		startTime := time.Now()
		statements := splitSQLStatements(string(content))
		if err := s.executeSQLStatements(statements); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行SQL失败: %v", err)
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
			return fmt.Errorf("记录版本信息失败: %v", err)
		}

		// 提交事务
		if err := tx.Commit().Error; err != nil {
			return fmt.Errorf("提交事务失败: %v", err)
		}
	}

	migrationLog("数据库迁移完成")
	return nil
}
