package config

import (
	"os"
	"testing"
)

func TestLoadStorageRequiresRegistrationSettings(t *testing.T) {
	for _, key := range []string{"NODE_ID", "NODE_NAME", "NODE_ADDR", "INTERNAL_TOKEN", "COORDINATOR_URL"} {
		old, had := os.LookupEnv(key)
		os.Unsetenv(key)
		defer func(k, v string, ok bool) {
			if ok {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}(key, old, had)
	}
	if _, err := Load("storage"); err == nil {
		t.Fatal("expected missing storage settings error")
	}
}
func TestLoadCoordinatorDefaultsDatabase(t *testing.T) {
	old, had := os.LookupEnv("DATABASE_URL")
	os.Unsetenv("DATABASE_URL")
	defer func() {
		if had {
			os.Setenv("DATABASE_URL", old)
		}
	}()
	t.Setenv("INTERNAL_TOKEN", "test-internal-token-123")
	c, err := Load("coordinator")
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "postgres://gostorage:gostorage@localhost:5432/gostorage_db?sslmode=disable" {
		t.Fatalf("unexpected database URL %q", c.DatabaseURL)
	}
}
