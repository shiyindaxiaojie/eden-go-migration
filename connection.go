package migration

import (
	"fmt"
	"log"
	"os"
	"time"

	sqlite "github.com/glebarez/sqlite" // Pure Go SQLite driver
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// DB 数据库实例
type DB struct {
	*gorm.DB
}

// InitDB 初始化数据库连接
func InitDB(cfg *DatabaseConfig) (*DB, error) {
	// 使用标准 logger
	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
		logger.Config{
			SlowThreshold:             time.Second, // Slow SQL threshold
			LogLevel:                  logger.Info, // Log level
			IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
			ParameterizedQueries:      true,        // Don't include params in the SQL log
			Colorful:                  true,        // Disable color
		},
	)

	// 首先创建数据库（SQLite 不需要）
	if cfg.Driver != "sqlite" && cfg.Driver != "sqlite3" {
		fmt.Printf("尝试创建数据库: %s\n", cfg.DBName)
		if err := createDatabase(cfg); err != nil {
			return nil, fmt.Errorf("创建数据库失败: %v", err)
		}
		fmt.Println("数据库创建成功或已存在")
	}

	dsn := cfg.GetDSN()
	safeDSN := cfg.GetSafeDSN()
	fmt.Printf("数据库连接 DSN: %s\n", safeDSN)

	// 配置 GORM
	gormConfig := &gorm.Config{
		Logger:                                   newLogger,
		DisableForeignKeyConstraintWhenMigrating: true, // 禁用外键约束
		NamingStrategy: schema.NamingStrategy{
			SingularTable: cfg.SingularTable,
		},
	}

	// 根据驱动类型选择对应的 dialector
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	case "sqlite", "sqlite3":
		dialector = sqlite.Open(dsn)
	default: // mysql, mariadb
		dialector = mysql.Open(dsn)
	}

	// 连接数据库
	fmt.Println("尝试连接数据库")
	gormDB, err := gorm.Open(dialector, gormConfig)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %v", err)
	}
	fmt.Println("数据库连接成功")

	db := &DB{DB: gormDB}

	// 获取底层的 *sql.DB 对象
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("获取 *sql.DB 失败: %v", err)
	}

	// 设置连接池参数
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	// 使用默认值设置连接最大生命周期
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// createDatabase 创建数据库
func createDatabase(cfg *DatabaseConfig) error {
	// SQLite 不需要创建数据库
	if cfg.Driver == "sqlite" || cfg.Driver == "sqlite3" {
		return nil
	}

	dsn := cfg.GetCreateDBDSN()

	// 根据驱动选择对应的 dialector
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	default: // mysql, mariadb
		dialector = mysql.Open(dsn)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true, // 禁用外键约束
	})
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	// 创建数据库
	var sql string
	switch cfg.Driver {
	case "postgres", "postgresql":
		// PostgreSQL 需要先检查数据库是否存在
		sql = fmt.Sprintf("CREATE DATABASE %s", cfg.DBName)
		// 对于 PostgreSQL，如果数据库已存在会报错，我们可以忽略
		if err := db.Exec(sql).Error; err != nil {
			// 忽略"数据库已存在"的错误
			if !isDatabaseExistsError(err) {
				return err
			}
		}
	default: // mysql, mariadb
		sql = fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", cfg.DBName)
		return db.Exec(sql).Error
	}

	return nil
}

// isDatabaseExistsError 检查是否是数据库已存在的错误
func isDatabaseExistsError(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL error code for duplicate database
	return err.Error() == "pq: database \""+err.Error()+"\" already exists" ||
		// 更通用的检查
		err.Error() != "" && (err.Error()[len(err.Error())-15:] == "already exists")
}
