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

// DB database instance
type DB struct {
	*gorm.DB
}

// InitDB initializes database connection
func InitDB(cfg *DatabaseConfig) (*DB, error) {
	// Use standard logger
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

	// First create database (SQLite does not need it)
	if cfg.Driver != "sqlite" && cfg.Driver != "sqlite3" {
		fmt.Printf("Attempting to create database: %s\n", cfg.DBName)
		if err := createDatabase(cfg); err != nil {
			return nil, fmt.Errorf("failed to create database: %v", err)
		}
		fmt.Println("Database created successfully or already exists")
	}

	dsn := cfg.GetDSN()
	safeDSN := cfg.GetSafeDSN()
	fmt.Printf("Database connection DSN: %s\n", safeDSN)

	// Configure GORM
	gormConfig := &gorm.Config{
		Logger:                                   newLogger,
		DisableForeignKeyConstraintWhenMigrating: true, // Disable foreign key constraints
		NamingStrategy: schema.NamingStrategy{
			SingularTable: cfg.SingularTable,
		},
	}

	// Select corresponding dialector based on driver type
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	case "sqlite", "sqlite3":
		dialector = sqlite.Open(dsn)
	default: // mysql, mariadb
		dialector = mysql.Open(dsn)
	}

	// Connect to database
	fmt.Println("Attempting to connect to database")
	gormDB, err := gorm.Open(dialector, gormConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %v", err)
	}
	fmt.Println("Database connected successfully")

	db := &DB{DB: gormDB}

	// Get underlying *sql.DB object
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get *sql.DB: %v", err)
	}

	// Set connection pool parameters
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	// Set connection max lifetime using default value
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// createDatabase creates database
func createDatabase(cfg *DatabaseConfig) error {
	// SQLite does not need to create database
	if cfg.Driver == "sqlite" || cfg.Driver == "sqlite3" {
		return nil
	}

	dsn := cfg.GetCreateDBDSN()

	// Select corresponding dialector based on driver
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	default: // mysql, mariadb
		dialector = mysql.Open(dsn)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true, // Disable foreign key constraints
	})
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	// Create database
	var sql string
	switch cfg.Driver {
	case "postgres", "postgresql":
		// PostgreSQL needs to check if database exists first
		sql = fmt.Sprintf("CREATE DATABASE %s", cfg.DBName)
		// For PostgreSQL, ignore error if database already exists
		if err := db.Exec(sql).Error; err != nil {
			// Ignore "database already exists" error
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

// isDatabaseExistsError checks if it is a database already exists error
func isDatabaseExistsError(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL error code for duplicate database
	return err.Error() == "pq: database \""+err.Error()+"\" already exists" ||
		// More generic check
		err.Error() != "" && (err.Error()[len(err.Error())-15:] == "already exists")
}
