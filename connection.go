package migration

import (
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

	// 首先创建数据库
	fmt.Printf("尝试创建数据库: %s\n", cfg.DBName)
	if err := createDatabase(cfg); err != nil {
		return nil, fmt.Errorf("创建数据库失败: %v", err)
	}
	fmt.Println("数据库创建成功或已存在")

	dsn := cfg.GetDSN()
	safeDSN := cfg.GetSafeDSN()
	fmt.Printf("数据库连接 DSN: %s\n", safeDSN)

	// 配置 GORM
	gormConfig := &gorm.Config{
		Logger:                                   newLogger,
		DisableForeignKeyConstraintWhenMigrating: true, // 禁用外键约束
	}

	// 连接数据库
	fmt.Println("尝试连接数据库")
	gormDB, err := gorm.Open(mysql.Open(dsn), gormConfig)
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
	dsn := cfg.GetCreateDBDSN()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
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
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", cfg.DBName)
	return db.Exec(sql).Error
}
