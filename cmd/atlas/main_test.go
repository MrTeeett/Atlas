package main

import (
	"os"
	"testing"
)

func TestEnvDefault(t *testing.T) {
	const key = "ATLAS_ENVDEFAULT_TEST"
	old, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if got := envDefault(key, "x"); got != "x" {
		t.Fatalf("expected fallback, got %q", got)
	}
	_ = os.Setenv(key, "y")
	if got := envDefault(key, "x"); got != "y" {
		t.Fatalf("expected env value, got %q", got)
	}
}
