package agent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type EnrollRequest struct {
	JoinToken string   `json:"join_token"`
	Name      string   `json:"name"`
	Tags      []string `json:"tags"`
	PublicKey string   `json:"public_key"`
	Hostname  string   `json:"hostname"`
}

type EnrollResponse struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
}

// Enroll generates a key pair, registers it with the hub using a join token
// and writes the agent config.
func Enroll(configPath, hubURL, joinToken, name string, tags []string) (*Config, error) {
	hubURL = strings.TrimRight(hubURL, "/")
	cfg := &Config{HubURL: hubURL, Name: name, Tags: tags}
	if old, err := os.ReadFile(configPath); err == nil && len(old) > 0 {
		// Keep any local tuning from a previous config.
		if prev, err := LoadConfig(configPath); err == nil {
			*cfg = *prev
			cfg.HubURL, cfg.Name, cfg.Tags, cfg.AgentID = hubURL, name, tags, ""
		}
	}
	cfg.applyDefaults()
	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	if cfg.Name == "" {
		cfg.Name = host
	}
	body, _ := json.Marshal(EnrollRequest{
		JoinToken: joinToken,
		Name:      cfg.Name,
		Tags:      tags,
		PublicKey: base64.StdEncoding.EncodeToString([]byte(key.Public().(ed25519.PublicKey))),
		Hostname:  host,
	})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(hubURL+"/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("contacting hub: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub refused enrollment (%s): %s", resp.Status, strings.TrimSpace(string(rb)))
	}
	var er EnrollResponse
	if err := json.Unmarshal(rb, &er); err != nil {
		return nil, fmt.Errorf("bad hub response: %w", err)
	}
	cfg.AgentID, cfg.Name = er.AgentID, er.Name
	// Only replace the key once the hub has accepted it.
	if err := SaveKey(cfg.KeyFile, key); err != nil {
		return nil, err
	}
	if err := SaveConfig(configPath, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
