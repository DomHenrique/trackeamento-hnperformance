package config

import (
	"fmt"
	"log"
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

// Validate executa verificação defensiva de segurança e emite alertas para senhas padrão
func (c *Config) Validate() error {
	strict := os.Getenv("STRICT_CONFIG_VALIDATION") == "true"

	var warnings []string
	if c.AdminPassword == "hn_admin_secret_pass_2026" {
		warnings = append(warnings, "ADMIN_PASSWORD está utilizando a senha padrão. Recomenda-se definir uma senha personalizada.")
	}
	if c.PostgresPassword == "postgres_secret_pass" {
		warnings = append(warnings, "POSTGRES_PASSWORD está utilizando a senha padrão.")
	}
	if c.RedisPassword == "redis_secret_pass" {
		warnings = append(warnings, "REDIS_PASSWORD está utilizando a senha padrão.")
	}
	if c.ClickHousePassword == "clickhouse_secret_pass" {
		warnings = append(warnings, "CLICKHOUSE_PASSWORD está utilizando a senha padrão.")
	}

	for _, w := range warnings {
		log.Printf("[SECURITY WARNING] %s", w)
	}

	if strict && len(warnings) > 0 {
		return fmt.Errorf("STRICT_CONFIG_VALIDATION: %d credenciais padrão detectadas em produção", len(warnings))
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
