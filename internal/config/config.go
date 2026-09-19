package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Env            string
	TrackingDomain string
	HTTPPort       string

	// Admin
	AdminUser     string
	AdminPassword string

	// Redis
	RedisAddr           string
	RedisPassword       string
	RedisStreamRaw      string
	RedisStreamDispatch string
	RedisConsumerGroup  string

	// Postgres
	PostgresHost     string
	PostgresPort     string
	PostgresDB       string
	PostgresUser     string
	PostgresPassword string
	PostgresSSLMode  string

	// ClickHouse
	ClickHouseAddr     string
	ClickHouseDB       string
	ClickHouseUser     string
	ClickHousePassword string

	// Security & Ingestion
	ServerKey  string
	HMACPepper string

	// Ingester
	IngesterBatchSize int
	IngesterFlushSec  int

	// Dispatcher
	DispatcherWorkers    int
	DispatcherMaxRetries int
	DispatcherTimeoutSec int
}

func Load() *Config {
	return &Config{
		Env:            getEnv("ENV", "development"),
		TrackingDomain: getEnv("TRACKING_DOMAIN", "localhost"),
		HTTPPort:       getEnv("HTTP_PORT", "8080"),
		AdminUser:     getEnv("ADMIN_USER", "admin"),
		AdminPassword: getEnv("ADMIN_PASSWORD", "hn_admin_secret_pass_2026"),

		ServerKey:  getEnv("SERVER_API_KEY", "hn_server_internal_secret_key"),
		HMACPepper: getEnv("HMAC_PEPPER", "hn_pepper_secret_salt_2026"),

		RedisAddr:           getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:       getEnv("REDIS_PASSWORD", "redis_secret_pass"),
		RedisStreamRaw:      getEnv("REDIS_STREAM_RAW", "stream:events:raw"),
		RedisStreamDispatch: getEnv("REDIS_STREAM_DISPATCH", "stream:events:dispatch"),
		RedisConsumerGroup:  getEnv("REDIS_CONSUMER_GROUP", "tracking_workers"),

		PostgresHost:     getEnv("POSTGRES_HOST", "127.0.0.1"),
		PostgresPort:     getEnv("POSTGRES_PORT", "5432"),
		PostgresDB:       getEnv("POSTGRES_DB", "tracking_db"),
		PostgresUser:     getEnv("POSTGRES_USER", "tracking_user"),
		PostgresPassword: getEnv("POSTGRES_PASSWORD", "postgres_secret_pass"),
		PostgresSSLMode:  getEnv("POSTGRES_SSLMODE", "disable"),

		ClickHouseAddr:     getEnv("CLICKHOUSE_ADDR", "127.0.0.1:9000"),
		ClickHouseDB:       getEnv("CLICKHOUSE_DB", "tracking_events"),
		ClickHouseUser:     getEnv("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getEnv("CLICKHOUSE_PASSWORD", "clickhouse_secret_pass"),

		IngesterBatchSize: getEnvAsInt("INGESTER_BATCH_SIZE", 2000),
		IngesterFlushSec:  getEnvAsInt("INGESTER_FLUSH_INTERVAL_SEC", 2),

		DispatcherWorkers:    getEnvAsInt("DISPATCHER_WORKERS", 8),
		DispatcherMaxRetries: getEnvAsInt("DISPATCHER_MAX_RETRIES", 5),
		DispatcherTimeoutSec: getEnvAsInt("DISPATCHER_TIMEOUT_SEC", 10),
	}
}

// Validate executa verificação defensiva de segurança fail-fast em ambiente de produção
func (c *Config) Validate() error {
	if c.Env == "production" {
		if c.AdminPassword == "hn_admin_secret_pass_2026" {
			return fmt.Errorf("ADMIN_PASSWORD não pode conter a senha padrão em produção")
		}
		if c.PostgresPassword == "postgres_secret_pass" {
			return fmt.Errorf("POSTGRES_PASSWORD não pode conter a senha padrão em produção")
		}
		if c.RedisPassword == "redis_secret_pass" {
			return fmt.Errorf("REDIS_PASSWORD não pode conter a senha padrão em produção")
		}
		if c.ClickHousePassword == "clickhouse_secret_pass" {
			return fmt.Errorf("CLICKHOUSE_PASSWORD não pode conter a senha padrão em produção")
		}
	}
	return nil
}

func (c *Config) PostgresDSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.PostgresHost, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDB, c.PostgresSSLMode)
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvAsInt(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}
