// Package agent implements csa-agent: it keeps an outbound WebSocket to the
// hub, enforces the local policy and runs tools on this server.
package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/csa/agent.yaml"

type Config struct {
	HubURL  string   `yaml:"hub_url"`
	AgentID string   `yaml:"agent_id"`
	Name    string   `yaml:"name"`
	Tags    []string `yaml:"tags,omitempty"`

	KeyFile      string `yaml:"key_file"`
	PolicyFile   string `yaml:"policy_file"`
	HubRulesFile string `yaml:"hub_rules_file"`
	AuditLog     string `yaml:"audit_log"`

	MaxOutputBytes    int `yaml:"max_output_bytes"`
	DefaultTimeoutSec int `yaml:"default_timeout_sec"`
	MaxTimeoutSec     int `yaml:"max_timeout_sec"`
	MaxReadBytes      int `yaml:"max_read_bytes"`
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
	def(&c.KeyFile, "/etc/csa/agent.key")
	def(&c.PolicyFile, "/etc/csa/policy.yaml")
	def(&c.HubRulesFile, "/var/lib/csa/hub-rules.yaml")
	def(&c.AuditLog, "/var/log/csa/audit.jsonl")
	defi(&c.MaxOutputBytes, 64*1024)
	defi(&c.DefaultTimeoutSec, 60)
	defi(&c.MaxTimeoutSec, 3600)
	defi(&c.MaxReadBytes, 512*1024)
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
	if c.HubURL == "" || c.AgentID == "" {
		return nil, errors.New("agent is not enrolled: run `csa-agent enroll` first")
	}
	c.HubURL = strings.TrimRight(c.HubURL, "/")
	return &c, nil
}

func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	hdr := "# csa-agent configuration. See https://github.com/cstone1983/Claude-server-agent\n"
	return os.WriteFile(path, append([]byte(hdr), b...), 0o600)
}

func LoadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: invalid key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func GenerateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return priv, err
}

func SaveKey(path string, priv ed25519.PrivateKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(priv.Seed())
	return os.WriteFile(path, []byte(enc+"\n"), 0o600)
}
