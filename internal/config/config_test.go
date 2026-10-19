package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:8090" || c.AllowPrivate || c.RetentionDays != 90 || c.MaxChecks != 200 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestPublicAddressNeedsToken(t *testing.T) {
	t.Setenv("WK_ADDR", "0.0.0.0:8090")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WK_API_TOKEN") {
		t.Fatalf("want a token error, got %v", err)
	}
	t.Setenv("WK_API_TOKEN", "short")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "32 characters") {
		t.Fatalf("want a token length error, got %v", err)
	}
	t.Setenv("WK_API_TOKEN", strings.Repeat("x", 32))
	if _, err := Load(); err != nil {
		t.Fatalf("valid token: %v", err)
	}
}

func TestInvalidValues(t *testing.T) {
	t.Setenv("WK_ALLOW_PRIVATE_TARGETS", "maybe")
	t.Setenv("WK_RETENTION_DAYS", "1")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WK_ALLOW_PRIVATE_TARGETS") || !strings.Contains(err.Error(), "WK_RETENTION_DAYS") {
		t.Fatalf("both errors should be reported together, got %v", err)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, "0.0.0.0:1": false, ":8090": false} {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v", addr, got)
		}
	}
}
