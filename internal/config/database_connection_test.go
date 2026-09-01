package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDatabaseConnectionConfigParsesOnlyDatabaseURL(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"DATABASE_URL": "postgres://forum:secret@db.example.test:5433/forum?sslmode=require&pool_max_conns=99",
	}
	configured, err := LoadDatabaseConnectionConfig(mapLookup(values))
	if err != nil {
		t.Fatalf("LoadDatabaseConnectionConfig() returned error: %v", err)
	}
	if configured.Host != "db.example.test" || configured.Port != 5433 || configured.Database != "forum" || configured.User != "forum" {
		t.Fatalf("connection identity = (%q, %d, %q, %q), want db.example.test:5433/forum as forum", configured.Host, configured.Port, configured.Database, configured.User)
	}
	if configured.ConnectTimeout != 5*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 5s", configured.ConnectTimeout)
	}
}

func TestLoadDatabaseConnectionConfigRejectsMissingAndRedactsInvalidURL(t *testing.T) {
	t.Parallel()

	const secret = "do-not-expose-migration-database-secret"
	tests := []struct {
		name   string
		lookup LookupEnv
	}{
		{name: "nil lookup"},
		{name: "missing value", lookup: mapLookup(map[string]string{})},
		{name: "empty value", lookup: mapLookup(map[string]string{"DATABASE_URL": ""})},
		{name: "invalid value", lookup: mapLookup(map[string]string{"DATABASE_URL": "postgres://" + secret + "%zz"})},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			configured, err := LoadDatabaseConnectionConfig(test.lookup)
			if err == nil || configured != nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("LoadDatabaseConnectionConfig() = (%+v, %v), want (nil, redacted error)", configured, err)
			}
		})
	}
}
