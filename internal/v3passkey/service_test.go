package v3passkey

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testConfig(db *pgxpool.Pool) Config {
	return Config{
		DB:          db,
		InstanceID:  "test-instance",
		RPID:        "aiops.example.invalid",
		Origin:      "https://aiops.example.invalid",
		Tier:        "nonprod",
		DisplayName: "AIOps Canvas",
	}
}

func TestConfigRequiresTrustedRPAndOrigin(t *testing.T) {
	db, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	valid := testConfig(db)
	if _, err := New(valid); err != nil {
		t.Fatalf("valid HTTPS RP configuration: %v", err)
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"missing database", func(c *Config) { c.DB = nil }},
		{"missing instance", func(c *Config) { c.InstanceID = "" }},
		{"missing RP ID", func(c *Config) { c.RPID = "" }},
		{"missing origin", func(c *Config) { c.Origin = "" }},
		{"HTTP origin", func(c *Config) { c.Origin = "http://aiops.example.invalid" }},
		{"wildcard origin", func(c *Config) { c.Origin = "https://*.example.invalid" }},
		{"foreign origin", func(c *Config) { c.Origin = "https://attacker.example.invalid" }},
		{"origin with path", func(c *Config) { c.Origin += "/app" }},
		{"origin with query", func(c *Config) { c.Origin += "?next=1" }},
		{"origin with userinfo", func(c *Config) { c.Origin = "https://user@aiops.example.invalid" }},
		{"invalid tier", func(c *Config) { c.Tier = "unknown" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.change(&c)
			if _, err := New(c); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("New() error = %v, want unavailable", err)
			}
		})
	}
}

func TestHTTPOriginNeedsExplicitLocalTestMode(t *testing.T) {
	db, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := testConfig(db)
	c.RPID, c.Origin = "localhost", "http://localhost:8080"
	if _, err := New(c); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("implicit local HTTP trust: %v", err)
	}
	c.AllowLocalhostHTTP = true
	if _, err := New(c); err != nil {
		t.Fatalf("explicit isolated localhost test mode: %v", err)
	}
	c.Origin = "http://aiops.example.invalid"
	if _, err := New(c); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("test mode trusted nonlocal HTTP: %v", err)
	}
}
