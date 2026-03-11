package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort            = "8080"
	defaultMaxPageSize     = 5000
	defaultResyncPeriod    = 10 * time.Minute
	defaultDiscoveryPeriod = 60 * time.Second
)

// Config holds runtime behavior configured by environment variables.
type Config struct {
	Port            string
	MaxPageSize     int
	ResyncPeriod    time.Duration
	DiscoveryPeriod time.Duration
	ExcludeGVRs     map[string]struct{}
	ExcludeNS       map[string]struct{}
}

func Load() (Config, error) {
	cfg := Config{
		Port:            getEnv("PORT", defaultPort),
		MaxPageSize:     defaultMaxPageSize,
		ResyncPeriod:    defaultResyncPeriod,
		DiscoveryPeriod: defaultDiscoveryPeriod,
		ExcludeGVRs:     parseSet(os.Getenv("EXCLUDE_GVRS")),
		ExcludeNS:       parseSet(os.Getenv("EXCLUDE_NAMESPACES")),
	}

	if err := parseIntEnv("MAX_PAGE_SIZE", &cfg.MaxPageSize, 100, 100000); err != nil {
		return Config{}, err
	}
	if err := parseDurationEnv("RESYNC_PERIOD", &cfg.ResyncPeriod); err != nil {
		return Config{}, err
	}
	if err := parseDurationEnv("DISCOVERY_INTERVAL", &cfg.DiscoveryPeriod); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func parseIntEnv(name string, out *int, min int, max int) error {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", name, err)
	}
	if v < min || v > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	*out = v
	return nil
}

func parseDurationEnv(name string, out *time.Duration) error {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s must be a Go duration string: %w", name, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	*out = d
	return nil
}

func parseSet(raw string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	return out
}

func getEnv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
