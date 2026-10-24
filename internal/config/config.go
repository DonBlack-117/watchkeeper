// Package config reads the settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/notify"
)

// DefaultDatabaseURL is the local docker-compose database; production must override it.
const DefaultDatabaseURL = "postgres://watchkeeper:watchkeeper@localhost:5432/watchkeeper?sslmode=disable" //nolint:gosec // local docker-compose credentials; main warns if used on a public address

// Config holds every setting; Load fills it from WK_* variables.
type Config struct {
	Addr           string         // WK_ADDR
	DatabaseURL    string         // WK_DATABASE_URL
	APIToken       string         // WK_API_TOKEN: required when Addr is not loopback
	AllowPrivate   bool           // WK_ALLOW_PRIVATE_TARGETS: let checks reach localhost and private networks
	MaxConcurrent  int            // WK_MAX_CONCURRENT
	MaxChecks      int            // WK_MAX_CHECKS
	ReloadInterval time.Duration  // WK_RELOAD_INTERVAL
	RetentionDays  int            // WK_RETENTION_DAYS
	LogLevel       slog.Level     // WK_LOG_LEVEL: debug, info, warn, error
	AdminUser      string         // WK_ADMIN_USER: dashboard account created or updated at startup
	AdminPassword  string         // WK_ADMIN_PASSWORD
	SecureCookie   bool           // WK_COOKIE_SECURE: default true unless Addr is loopback
	TrustedProxies []netip.Prefix // WK_TRUSTED_PROXIES: comma-separated CIDRs or IPs

	TelegramToken   string // WK_TELEGRAM_BOT_TOKEN: from @BotFather
	TelegramChatID  string // WK_TELEGRAM_CHAT_ID: user, group or channel id
	AlertOnDegraded bool   // WK_ALERT_ON_DEGRADED: also alert when a check is only degraded
	PublicURL       string // WK_PUBLIC_URL: dashboard link added to alerts
	MetricsAddr     string // WK_METRICS_ADDR: Prometheus /metrics listener; empty disables it

	AnthropicKey     string // WK_ANTHROPIC_API_KEY (or ANTHROPIC_API_KEY): enables AI triage
	TriageModel      string // WK_TRIAGE_MODEL
	TriageEffort     string // WK_TRIAGE_EFFORT: low, medium, high, xhigh or max
	TriageAuto       bool   // WK_TRIAGE_AUTO: investigate each incident when it goes down
	TriageDailyLimit int    // WK_TRIAGE_DAILY_LIMIT: investigations per 24 hours
}

// DefaultTriageModel is the Claude model used for incident triage.
const DefaultTriageModel = "claude-opus-5-5"

// Load reads the environment. It listens on localhost by default and refuses to
// listen elsewhere without an API token or a dashboard account.
func Load() (Config, error) {
	c := Config{
		Addr:           env("WK_ADDR", "127.0.0.1:8090"),
		DatabaseURL:    env("WK_DATABASE_URL", DefaultDatabaseURL),
		APIToken:       os.Getenv("WK_API_TOKEN"),
		AdminUser:      os.Getenv("WK_ADMIN_USER"),
		AdminPassword:  os.Getenv("WK_ADMIN_PASSWORD"),
		TelegramToken:  os.Getenv("WK_TELEGRAM_BOT_TOKEN"),
		TelegramChatID: os.Getenv("WK_TELEGRAM_CHAT_ID"),
		PublicURL:      os.Getenv("WK_PUBLIC_URL"),
		MetricsAddr:    os.Getenv("WK_METRICS_ADDR"),
		ReloadInterval: 30 * time.Second,
		AnthropicKey:   env("WK_ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY")),
		TriageModel:    env("WK_TRIAGE_MODEL", DefaultTriageModel),
		TriageEffort:   env("WK_TRIAGE_EFFORT", "high"),
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
	if (c.AdminUser == "") != (c.AdminPassword == "") {
		errs = append(errs, errors.New("set both WK_ADMIN_USER and WK_ADMIN_PASSWORD, or neither"))
	}
	if c.AdminPassword != "" && (len(c.AdminPassword) < 12 || len(c.AdminPassword) > 72) {
		errs = append(errs, errors.New("WK_ADMIN_PASSWORD must be between 12 and 72 bytes"))
	}
	if len(c.AdminUser) > 64 {
		errs = append(errs, errors.New("WK_ADMIN_USER must be at most 64 characters"))
	}
	if !IsLoopback(c.Addr) && c.APIToken == "" && c.AdminUser == "" {
		errs = append(errs, fmt.Errorf("WK_ADDR %q is not a loopback address: set WK_API_TOKEN or WK_ADMIN_USER before exposing the API", c.Addr))
	}
	if c.SecureCookie, err = boolEnv("WK_COOKIE_SECURE", !IsLoopback(c.Addr)); err != nil {
		errs = append(errs, err)
	} else if !c.SecureCookie && !IsLoopback(c.Addr) {
		errs = append(errs, errors.New("WK_COOKIE_SECURE=false is only allowed on a loopback WK_ADDR; serve the dashboard over HTTPS"))
	}
	if c.TrustedProxies, err = prefixes(os.Getenv("WK_TRUSTED_PROXIES")); err != nil {
		errs = append(errs, err)
	}
	if (c.TelegramToken == "") != (c.TelegramChatID == "") {
		errs = append(errs, errors.New("set both WK_TELEGRAM_BOT_TOKEN and WK_TELEGRAM_CHAT_ID, or neither"))
	}
	if c.TelegramToken != "" && !notify.ValidTelegramToken(c.TelegramToken) {
		errs = append(errs, errors.New("WK_TELEGRAM_BOT_TOKEN does not look like a bot token (<digits>:<secret>)"))
	}
	if c.TelegramChatID != "" && !chatID.MatchString(c.TelegramChatID) {
		errs = append(errs, errors.New("WK_TELEGRAM_CHAT_ID must be a numeric id (negative for groups) or @channelname"))
	}
	if c.AlertOnDegraded, err = boolEnv("WK_ALERT_ON_DEGRADED", false); err != nil {
		errs = append(errs, err)
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("WK_PUBLIC_URL must be an absolute http or https URL, got %q", c.PublicURL))
		}
	}
	if c.MetricsAddr != "" {
		if _, _, err := net.SplitHostPort(c.MetricsAddr); err != nil {
			errs = append(errs, fmt.Errorf("WK_METRICS_ADDR must be host:port, got %q", c.MetricsAddr))
		} else if c.MetricsAddr == c.Addr {
			errs = append(errs, errors.New("WK_METRICS_ADDR must differ from WK_ADDR, so /metrics stays off the public port"))
		}
	}
	if c.TriageAuto, err = boolEnv("WK_TRIAGE_AUTO", true); err != nil {
		errs = append(errs, err)
	}
	if c.TriageDailyLimit, err = intEnv("WK_TRIAGE_DAILY_LIMIT", 20, 1, 1000); err != nil {
		errs = append(errs, err)
	}
	switch c.TriageEffort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		errs = append(errs, fmt.Errorf("WK_TRIAGE_EFFORT must be low, medium, high, xhigh or max, got %q", c.TriageEffort))
	}
	if !triageModel.MatchString(c.TriageModel) {
		errs = append(errs, fmt.Errorf("WK_TRIAGE_MODEL %q is not a model id", c.TriageModel))
	}
	if c.AnthropicKey != "" && (len(c.AnthropicKey) < 20 || strings.ContainsAny(c.AnthropicKey, " \t\r\n")) {
		errs = append(errs, errors.New("WK_ANTHROPIC_API_KEY does not look like an API key"))
	}
	return c, errors.Join(errs...)
}

// triageModel accepts model ids such as claude-opus-5-5.
var triageModel = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,99}$`)

// chatID accepts a numeric chat id (negative for groups) or a public @channel.
var chatID = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

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

// prefixes parses a comma-separated list of CIDRs; a bare IP becomes a single-address prefix.
func prefixes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("WK_TRUSTED_PROXIES: %q is not an IP or CIDR", f)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("WK_TRUSTED_PROXIES: %q is not an IP or CIDR", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
