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
