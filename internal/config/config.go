// Package config reads the settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"
)

// DefaultDatabaseURL is the local docker-compose database; production must override it.
const DefaultDatabaseURL = "postgres://watchkeeper:watchkeeper@localhost:5432/watchkeeper?sslmode=disable" //nolint:gosec // local docker-compose credentials; main warns if used on a public address

// Config holds every setting; Load fills it from WK_* variables.
type Config struct {
	Addr           string        // WK_ADDR
	DatabaseURL    string        // WK_DATABASE_URL
	APIToken       string        // WK_API_TOKEN: required when Addr is not loopback
	AllowPrivate   bool          // WK_ALLOW_PRIVATE_TARGETS: let checks reach localhost and private networks
	MaxConcurrent  int           // WK_MAX_CONCURRENT
	MaxChecks      int           // WK_MAX_CHECKS
	ReloadInterval time.Duration // WK_RELOAD_INTERVAL
	RetentionDays  int           // WK_RETENTION_DAYS
	LogLevel       slog.Level    // WK_LOG_LEVEL: debug, info, warn, error
}

// Load reads the environment. It listens on localhost by default and refuses to
// listen elsewhere without an API token.
func Load() (Config, error) {
	c := Config{
		Addr:           env("WK_ADDR", "127.0.0.1:8090"),
		DatabaseURL:    env("WK_DATABASE_URL", DefaultDatabaseURL),
		APIToken:       os.Getenv("WK_API_TOKEN"),
		ReloadInterval: 30 * time.Second,
	}
	var errs []error
	var err error
	if c.AllowPrivate, err = boolEnv("WK_ALLOW_PRIVATE_TARGETS", false); err != nil {
		errs = append(errs, err)
	}
	if c.MaxConcurrent, err = intEnv("WK_MAX_CONCURRENT", 10, 1, 100); err != nil {
		errs = append(errs, err)
	}
	if c.MaxChecks, err = intEnv("WK_MAX_CHECKS", 200, 1, 10000); err != nil {
		errs = append(errs, err)
	}
	if c.RetentionDays, err = intEnv("WK_RETENTION_DAYS", 90, 7, 3650); err != nil {
		errs = append(errs, err)
	}
	if v := os.Getenv("WK_RELOAD_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			errs = append(errs, fmt.Errorf("WK_RELOAD_INTERVAL must be a duration of at least 1s, got %q", v))
		}
		c.ReloadInterval = d
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("WK_LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("WK_LOG_LEVEL: %w", err))
	}
	if c.APIToken != "" && len(c.APIToken) < 32 {
		errs = append(errs, errors.New("WK_API_TOKEN must be at least 32 characters (try: openssl rand -hex 32)"))
	}
	if !IsLoopback(c.Addr) && c.APIToken == "" {
		errs = append(errs, fmt.Errorf("WK_ADDR %q is not a loopback address: set WK_API_TOKEN before exposing the API", c.Addr))
	}
	return c, errors.Join(errs...)
}

// IsLoopback reports whether addr (host:port) only accepts local connections.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def, lo, hi int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def, fmt.Errorf("%s must be an integer between %d and %d, got %q", key, lo, hi, v)
	}
	return n, nil
}

func boolEnv(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s must be true or false, got %q", key, v)
	}
	return b, nil
}
