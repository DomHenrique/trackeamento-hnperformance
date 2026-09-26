package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
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

	// Declarative Bootstrap (Optional)
	SeedDefaultSite   bool
	DefaultClientName string
	DefaultSiteName   string
	DefaultSiteDomain string
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

		SeedDefaultSite:   getEnvAsBool("SEED_DEFAULT_SITE", false),
		DefaultClientName: getEnv("DEFAULT_CLIENT_NAME", "Minha Organização"),
		DefaultSiteName:   getEnv("DEFAULT_SITE_NAME", "Meu Site Principal"),
		DefaultSiteDomain: getEnv("DEFAULT_SITE_DOMAIN", ""),
	}
}

func isWeakOrPlaceholder(val, defaultVal string, minLen int) (bool, string) {
	valTrim := strings.TrimSpace(val)
	if valTrim == "" {
		return true, "está vazio"
	}
	if defaultVal != "" && valTrim == defaultVal {
		return true, fmt.Sprintf("está utilizando o valor padrão inseguro '%s'", defaultVal)
	}
	lower := strings.ToLower(valTrim)
	if strings.HasPrefix(lower, "change_") || strings.HasPrefix(lower, "generate_") {
		return true, "está utilizando um placeholder de exemplo que deve ser substituído"
	}
	if lower == "admin" || lower == "password" || lower == "123456" || lower == "root" || lower == "test" {
		return true, "está utilizando uma senha fraca trivial"
	}
	if len(valTrim) < minLen {
		return true, fmt.Sprintf("tem comprimento insuficiente (%d caracteres, mínimo exigido: %d)", len(valTrim), minLen)
	}
	return false, ""
}

// Validate executa verificação defensiva de segurança e emite erro fatal em produção se houver credenciais padrão ou fracas
func (c *Config) Validate() error {
	isProd := strings.EqualFold(c.Env, "production")
	strict := isProd || os.Getenv("STRICT_CONFIG_VALIDATION") == "true"

	type fieldCheck struct {
		name       string
		val        string
		defaultVal string
		minLen     int
	}

	checks := []fieldCheck{
		{"ADMIN_PASSWORD", c.AdminPassword, "hn_admin_secret_pass_2026", 12},
		{"POSTGRES_PASSWORD", c.PostgresPassword, "postgres_secret_pass", 12},
		{"REDIS_PASSWORD", c.RedisPassword, "redis_secret_pass", 12},
		{"CLICKHOUSE_PASSWORD", c.ClickHousePassword, "clickhouse_secret_pass", 12},
		{"SERVER_API_KEY", c.ServerKey, "hn_server_internal_secret_key", 16},
		{"HMAC_PEPPER", c.HMACPepper, "hn_pepper_secret_salt_2026", 16},
	}

	var issues []string
	for _, chk := range checks {
		if invalid, reason := isWeakOrPlaceholder(chk.val, chk.defaultVal, chk.minLen); invalid {
			issues = append(issues, fmt.Sprintf("%s %s", chk.name, reason))
		}
	}

	for _, issue := range issues {
		log.Printf("[SECURITY WARNING] %s", issue)
	}

	if strict && len(issues) > 0 {
		return fmt.Errorf("falha na validação de segurança de produção: %s", strings.Join(issues, "; "))
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

func getEnvAsBool(key string, defaultVal bool) bool {
	valStr := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if valStr == "" {
		return defaultVal
	}
	return valStr == "true" || valStr == "1" || valStr == "yes"
}

