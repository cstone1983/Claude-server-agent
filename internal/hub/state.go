package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

type AgentRec struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Tags         []string  `json:"tags"`
	PublicKey    string    `json:"public_key"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	Kernel       string    `json:"kernel"`
	Arch         string    `json:"arch"`
	AgentVersion string    `json:"agent_version"`
	EnrolledAt   time.Time `json:"enrolled_at"`
	LastSeen     time.Time `json:"last_seen"`
}

func (a *AgentRec) HasTag(t string) bool {
	for _, x := range a.Tags {
		if x == t {
			return true
		}
	}
	return false
}

type JoinToken struct {
	ExpiresAt time.Time `json:"expires_at"`
	UsesLeft  int       `json:"uses_left"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type OAuthClient struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"created_at"`
}

const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
	TokenAPI     = "api"

	ScopeFull = "full"
	ScopeRead = "read"
)

type Token struct {
	Kind      string    `json:"kind"`
	ClientID  string    `json:"client_id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Scope     string    `json:"scope"`
	Servers   []string  `json:"servers,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// HubRule is an "always allow" rule created from an approval and pushed to
// the agents in Scope ("all", "server:<name>" or "tag:<tag>").
type HubRule struct {
	ID         string      `json:"id"`
	Scope      string      `json:"scope"`
	Rule       policy.Rule `json:"rule"`
	ApprovalID string      `json:"approval_id,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	// Disabled rules are kept but not pushed to agents.
	Disabled  bool      `json:"disabled,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// carefulTag marks servers (Proxmox hosts) that only get rules approved for
// them specifically: "all servers" rules are not applied there.
const carefulTag = "proxmox"

func (r *HubRule) appliesTo(a *AgentRec) bool {
	switch {
	case r.Scope == "all":
		return !a.HasTag(carefulTag)
	case len(r.Scope) > 7 && r.Scope[:7] == "server:":
		return r.Scope[7:] == a.Name
	case len(r.Scope) > 4 && r.Scope[:4] == "tag:":
		return a.HasTag(r.Scope[4:])
	}
	return false
}

type stateData struct {
	Agents     map[string]*AgentRec    `json:"agents"`
	JoinTokens map[string]*JoinToken   `json:"join_tokens"`
	Clients    map[string]*OAuthClient `json:"oauth_clients"`
	Tokens     map[string]*Token       `json:"tokens"`
	Rules      []*HubRule              `json:"rules"`
	// Sessions are dashboard logins: hashed token -> expiry.
	Sessions map[string]time.Time `json:"dashboard_sessions"`
}

// Store is the hub's persistent state, saved as JSON. Secrets are stored
// only as SHA-256 hashes.
type Store struct {
	mu   sync.Mutex
	path string
	d    stateData
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.d.Agents == nil {
		s.d.Agents = map[string]*AgentRec{}
	}
	if s.d.JoinTokens == nil {
		s.d.JoinTokens = map[string]*JoinToken{}
	}
	if s.d.Clients == nil {
		s.d.Clients = map[string]*OAuthClient{}
	}
	if s.d.Tokens == nil {
		s.d.Tokens = map[string]*Token{}
	}
	if s.d.Sessions == nil {
		s.d.Sessions = map[string]time.Time{}
	}
	return s, nil
}

// Update runs fn under the lock and persists the result.
func (s *Store) Update(fn func(d *stateData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.d); err != nil {
		return err
	}
	return s.saveLocked()
}

// View runs fn under the lock without saving.
func (s *Store) View(fn func(d *stateData)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.d)
}

func (s *Store) saveLocked() error {
	now := time.Now()
	for k, t := range s.d.Tokens {
		if !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt) {
			delete(s.d.Tokens, k)
		}
	}
	for k, t := range s.d.JoinTokens {
		if now.After(t.ExpiresAt) || t.UsesLeft <= 0 {
			delete(s.d.JoinTokens, k)
		}
	}
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomToken(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func randomID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
