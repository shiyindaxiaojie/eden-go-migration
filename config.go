package migration

import (
	"fmt"
)

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Host         string `json:"host" mapstructure:"host"`
	Port         int    `json:"port" mapstructure:"port"`
	Username     string `json:"username" mapstructure:"username"`
	Password     string `json:"password" mapstructure:"password"`
	DBName       string `json:"db_name" mapstructure:"db_name"`
	MaxIdleConns int    `json:"max_idle_conns" mapstructure:"max_idle_conns"`
	MaxOpenConns int    `json:"max_open_conns" mapstructure:"max_open_conns"`
}

// DefaultDatabaseConfig 默认数据库配置
func DefaultDatabaseConfig() *DatabaseConfig {
	return &DatabaseConfig{
		Host:         "localhost",
		Port:         3306,
		Username:     "root",
		Password:     "",
		DBName:       "eden_cloud",
		MaxIdleConns: 10,
		MaxOpenConns: 100,
	}
}

// GetDSN 获取数据库连接字符串
func (c *DatabaseConfig) GetDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&allowNativePasswords=true",
		c.Username, c.Password, c.Host, c.Port, c.DBName)
}

// GetSafeDSN 获取安全的数据库连接字符串（隐藏密码）
func (c *DatabaseConfig) GetSafeDSN() string {
	return fmt.Sprintf("%s:***@tcp(%s:%d)/%s",
		c.Username, c.Host, c.Port, c.DBName)
}

// GetCreateDBDSN 获取用于创建数据库的连接字符串
func (c *DatabaseConfig) GetCreateDBDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/",
		c.Username, c.Password, c.Host, c.Port)
}