package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{"PORT", "MAX_PAGE_SIZE", "RESYNC_PERIOD", "EXCLUDE_GVRS", "EXCLUDE_NAMESPACES"} {
		_ = os.Unsetenv(key)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.Port != "8080" {
		t.Fatalf("unexpected default port: %s", cfg.Port)
	}
}
