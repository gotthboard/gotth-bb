package config

import (
	"strings"
	"testing"
	"time"
)

func TestDatabasePoolConfigAppliesBoundedLimits(t *testing.T) {
	t.Parallel()

	configured, err := Load(mapLookup(validConfigEnvironment()))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	poolConfig, err := configured.DatabasePoolConfig()
	if err != nil {
		t.Fatalf("DatabasePoolConfig() returned error: %v", err)
	}
	if poolConfig.MaxConns != 10 || poolConfig.MinConns != 0 || poolConfig.MaxConnLifetime != 30*time.Minute || poolConfig.MaxConnIdleTime != 5*time.Minute || poolConfig.HealthCheckPeriod != 30*time.Second || poolConfig.PingTimeout != 2*time.Second {
		t.Fatalf("pool limits = %+v", poolConfig)
	}
}

func TestDatabasePoolConfigRedactsParseFailure(t *testing.T) {
	t.Parallel()

	const databaseSecret = "do-not-expose-database-secret"
	configured := Config{databaseURL: secret{value: "postgres://" + databaseSecret + "%zz"}}
	_, err := configured.DatabasePoolConfig()
	if err == nil || strings.Contains(err.Error(), databaseSecret) {
		t.Fatalf("DatabasePoolConfig() error = %v", err)
	}
}

func TestDatabasePoolConfigRejectsMissingSecret(t *testing.T) {
	t.Parallel()

	poolConfig, err := (Config{}).DatabasePoolConfig()
	if err == nil {
		t.Fatalf("DatabasePoolConfig() = %+v, want error", poolConfig)
	}
	if poolConfig != nil {
		t.Fatalf("DatabasePoolConfig() config = %+v, want nil", poolConfig)
	}
}

func TestDatabasePoolConfigOverridesConnectionStringPoolLimits(t *testing.T) {
	t.Parallel()

	environment := validConfigEnvironment()
	environment["DATABASE_URL"] = "postgres://forum@127.0.0.1/forum?connect_timeout=99&pool_max_conns=99&pool_min_conns=8&pool_min_idle_conns=7&pool_max_conn_lifetime=12h&pool_max_conn_lifetime_jitter=1h&pool_max_conn_idle_time=2h&pool_health_check_period=30m"
	configured, err := Load(mapLookup(environment))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	poolConfig, err := configured.DatabasePoolConfig()
	if err != nil {
		t.Fatalf("DatabasePoolConfig() returned error: %v", err)
	}
	if poolConfig.MaxConns != 10 || poolConfig.MinConns != 0 || poolConfig.MinIdleConns != 0 || poolConfig.MaxConnLifetime != 30*time.Minute || poolConfig.MaxConnLifetimeJitter != 0 || poolConfig.MaxConnIdleTime != 5*time.Minute || poolConfig.HealthCheckPeriod != 30*time.Second || poolConfig.PingTimeout != 2*time.Second || poolConfig.ConnConfig.ConnectTimeout != 5*time.Second {
		t.Fatalf("pool limits = %+v", poolConfig)
	}
}
