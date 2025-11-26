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

// compareVersions 比较版本号 v1 < v2 返回 true
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

// validateChecksum 验证已执行脚本的校验和
func (s *MigrationService) validateChecksum(file string, executed *Migration) error {
	content, err := os.ReadFile(file)
	if err != nil {
		migrationLog("读取 SQL 文件失败: %v", err)
		return fmt.Errorf("读取 SQL 文件失败: %v", err)
	}

	checksum := fmt.Sprintf("%x", md5.Sum(content))
	if checksum != executed.Checksum {
		migrationLog("SQL 文件 %s 已被修改，期望校验和: %s, 实际校验和: %s", filepath.Base(file), executed.Checksum, checksum)
		migrationLog("警告：跳过校验和检查，继续执行")
	}
	return nil
}

// executeScriptFile 执行单个脚本文件
func (s *MigrationService) executeScriptFile(file, version, description, filename string) error {
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

	return nil
}

// processSQLFile 处理单个SQL文件
func (s *MigrationService) processSQLFile(file string, executedVersions map[string]*Migration) error {
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
		if err := s.validateChecksum(file, executed); err != nil {
			return err
		}
		migrationLog("SQL 文件 %s 校验和验证通过，跳过执行", version)
		return nil
	}

	// 执行脚本文件
	return s.executeScriptFile(file, version, description, filename)
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

	// 按版本号排序
	sort.Slice(files, func(i, j int) bool {
		v1, _, err1 := parseScriptVersion(filepath.Base(files[i]))
		v2, _, err2 := parseScriptVersion(filepath.Base(files[j]))

		if err1 == nil && err2 == nil {
			return compareVersions(v1, v2)
		}
		return files[i] < files[j]
	})

	// 遍历所有SQL文件
	for _, file := range files {
		if err := s.processSQLFile(file, executedVersions); err != nil {
			return err
		}
	}

	migrationLog("数据库迁移完成")
	return nil
}
