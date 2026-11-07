// Package hub implements csa-hub: the single public endpoint. It serves the
// MCP connector for Claude, a REST API for automations, OAuth login, the
// approval pages, and accepts outbound WebSocket connections from agents.
package hub

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/csa-hub/hub.yaml"

type Config struct {
	// Listen is the local address. cloudflared forwards the public hostname here.
	Listen string `yaml:"listen"`
	// PublicURL is the https URL people and agents use, e.g. https://agents.example.com
	PublicURL   string `yaml:"public_url"`
	StateDir    string `yaml:"state_dir"`
	AdminSocket string `yaml:"admin_socket"`
	AuditLog    string `yaml:"audit_log"`

	AdminPasswordHash string `yaml:"admin_password_hash"`
	TOTPSecret        string `yaml:"totp_secret"`

	// ApprovalWaitSec is how long a tool call waits for you to approve before
	// returning "pending" to Claude.
	ApprovalWaitSec int `yaml:"approval_wait_sec"`
	// ApprovalTTLMin is how long a pending approval stays valid.
	ApprovalTTLMin int `yaml:"approval_ttl_min"`
	// CallTimeoutSec bounds a single round trip to an agent.
	CallTimeoutSec int `yaml:"call_timeout_sec"`

	AccessTokenTTLMin   int `yaml:"access_token_ttl_min"`
	RefreshTokenTTLDays int `yaml:"refresh_token_ttl_days"`
	// AllowedRedirectHosts limits which OAuth clients may register.
	AllowedRedirectHosts []string `yaml:"allowed_redirect_hosts"`

	Notify NotifyConfig `yaml:"notify"`
}

type NotifyConfig struct {
	// NtfyURL is a full topic URL, e.g. https://ntfy.sh/my-secret-topic
	NtfyURL   string `yaml:"ntfy_url,omitempty"`
	NtfyToken string `yaml:"ntfy_token,omitempty"`
	// WebhookURL receives a JSON POST for every approval request (n8n, Home Assistant, ...).
	WebhookURL string `yaml:"webhook_url,omitempty"`
}

func (c *Config) applyDefaults() {
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	defi := func(i *int, v int) {
		if *i <= 0 {
			*i = v
		}
	}
	def(&c.Listen, "127.0.0.1:7420")
	def(&c.StateDir, "/var/lib/csa-hub")
	def(&c.AdminSocket, "/run/csa-hub/admin.sock")
	def(&c.AuditLog, "/var/log/csa-hub/audit.jsonl")
	defi(&c.ApprovalWaitSec, 45)
	defi(&c.ApprovalTTLMin, 24*60)
	defi(&c.CallTimeoutSec, 3700)
	defi(&c.AccessTokenTTLMin, 60)
	defi(&c.RefreshTokenTTLDays, 30)
	if len(c.AllowedRedirectHosts) == 0 {
		c.AllowedRedirectHosts = []string{"claude.ai", "claude.com", "localhost", "127.0.0.1"}
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.applyDefaults()
	if c.PublicURL == "" {
		return nil, fmt.Errorf("%s: public_url is required", path)
	}
	if u, err := url.Parse(c.PublicURL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: invalid public_url %q", path, c.PublicURL)
	}
	if c.AdminPasswordHash == "" || c.TOTPSecret == "" {
		return nil, fmt.Errorf("%s: admin password/TOTP not set; run `csa-hub init`", path)
	}
	return &c, nil
}

func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	hdr := "# csa-hub configuration. Keep this file private (mode 0600).\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(hdr), b...), 0o600); err != nil {
		return err
	}
	// Keep the file readable by the service user: match the directory's owner
	// (the installer makes /etc/csa-hub owned by csa-hub).
	if st, err := os.Stat(filepath.Dir(path)); err == nil {
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			os.Chown(tmp, int(s.Uid), int(s.Gid))
		}
	}
	return os.Rename(tmp, path)
}

func (c *Config) publicHost() string {
	u, _ := url.Parse(c.PublicURL)
	return u.Host
}
