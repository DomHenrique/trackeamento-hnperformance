package config

import (
	"strings"
	"testing"
)

func TestValidate_DevelopmentWithDefaults(t *testing.T) {
	cfg := &Config{
		Env:                "development",
		AdminPassword:      "hn_admin_secret_pass_2026",
		PostgresPassword:   "postgres_secret_pass",
		RedisPassword:      "redis_secret_pass",
		ClickHousePassword: "clickhouse_secret_pass",
		ServerKey:          "hn_server_internal_secret_key",
		HMACPepper:         "hn_pepper_secret_salt_2026",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Esperava que Validate() em development não falhasse com defaults, mas retornou erro: %v", err)
	}
}

func TestValidate_ProductionWithDefaults(t *testing.T) {
	cfg := &Config{
		Env:                "production",
		AdminPassword:      "hn_admin_secret_pass_2026",
		PostgresPassword:   "postgres_secret_pass",
		RedisPassword:      "redis_secret_pass",
		ClickHousePassword: "clickhouse_secret_pass",
		ServerKey:          "hn_server_internal_secret_key",
		HMACPepper:         "hn_pepper_secret_salt_2026",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Esperava que Validate() em production falhasse devido a credenciais padrão, mas retornou nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "ADMIN_PASSWORD") || !strings.Contains(errMsg, "POSTGRES_PASSWORD") {
		t.Errorf("Mensagem de erro não detalhou as credenciais violadas: %s", errMsg)
	}
}

func TestValidate_ProductionWithPlaceholders(t *testing.T) {
	cfg := &Config{
		Env:                "production",
		AdminPassword:      "change_this_admin_password_min_16_chars",
		PostgresPassword:   "generate_strong_postgres_password_here",
		RedisPassword:      "redis_secret_strong_custom_password_123!",
		ClickHousePassword: "clickhouse_strong_custom_password_123!",
		ServerKey:          "custom_secret_key_server_token_long_32_chars!",
		HMACPepper:         "custom_pepper_salt_string_super_secure_32!",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Esperava que Validate() falhasse com placeholders, mas retornou nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "placeholder") {
		t.Errorf("Esperava menção a placeholder no erro, obteve: %s", errMsg)
	}
}

func TestValidate_ProductionWithShortPassword(t *testing.T) {
	cfg := &Config{
		Env:                "production",
		AdminPassword:      "curta123", // menos de 12 chars
		PostgresPassword:   "postgres_strong_password_custom_123",
		RedisPassword:      "redis_strong_password_custom_123",
		ClickHousePassword: "clickhouse_strong_password_custom_123",
		ServerKey:          "custom_secret_key_server_token_long_32_chars!",
		HMACPepper:         "custom_pepper_salt_string_super_secure_32!",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Esperava que Validate() falhasse com senha curta, mas retornou nil")
	}

	if !strings.Contains(err.Error(), "comprimento insuficiente") {
		t.Errorf("Esperava menção a comprimento insuficiente, obteve: %s", err.Error())
	}
}

func TestValidate_ProductionSuccessWithStrongCredentials(t *testing.T) {
	cfg := &Config{
		Env:                "production",
		AdminPassword:      "Admin_Super_Seguro_Long_Pass_2026!#",
		PostgresPassword:   "Postgres_Prod_Secret_Db_Pass_2026!#",
		RedisPassword:      "Redis_Prod_Secret_Stream_Pass_2026!#",
		ClickHousePassword: "ClickHouse_Prod_Secret_Events_Pass_2026!#",
		ServerKey:          "a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6",
		HMACPepper:         "z9y8x7w6v5u4t3s2r1q0p1o2n3m4l5k6",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Esperava que Validate() passasse com credenciais fortes em produção, mas falhou: %v", err)
	}
}

func TestBootstrapConfig_Defaults(t *testing.T) {
	cfg := Load()
	if cfg.SeedDefaultSite {
		t.Errorf("Esperava SeedDefaultSite == false por padrão, obteve true")
	}
	if cfg.DefaultClientName != "Minha Organização" {
		t.Errorf("Esperava DefaultClientName == 'Minha Organização', obteve %s", cfg.DefaultClientName)
	}
}

func TestGetEnvAsBool(t *testing.T) {
	cases := []struct {
		val      string
		def      bool
		expected bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"", true, true},
		{"", false, false},
		{"invalid", false, false},
	}

	for _, c := range cases {
		t.Setenv("TEST_BOOL_VAR", c.val)
		res := getEnvAsBool("TEST_BOOL_VAR", c.def)
		if res != c.expected {
			t.Errorf("getEnvAsBool(%q, %v) = %v; esperado %v", c.val, c.def, res, c.expected)
		}
	}
}

