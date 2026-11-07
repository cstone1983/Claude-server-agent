// Package proto defines the messages exchanged between hub and agent over the
// agent's outbound WebSocket connection.
package proto

import (
	"encoding/json"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

// Message types.
const (
	TypeHello    = "hello"     // agent -> hub: host facts after connecting
	TypeCall     = "call"      // hub -> agent: run a tool
	TypeResult   = "result"    // agent -> hub: tool result
	TypeSetRules = "set_rules" // hub -> agent: replace hub-managed allow rules
)

// Connect handshake headers (agent -> hub).
const (
	HeaderAgentID   = "X-CSA-Agent-ID"
	HeaderTimestamp = "X-CSA-Timestamp"
	HeaderSignature = "X-CSA-Signature"
)

// SigningPayload is the string the agent signs with its Ed25519 key on connect.
func SigningPayload(agentID, ts string) []byte {
	return []byte("csa-connect\n" + agentID + "\n" + ts)
}

// Envelope is the single wire format; Type selects which fields are set.
type Envelope struct {
	Type   string        `json:"type"`
	ID     string        `json:"id,omitempty"`
	Hello  *Hello        `json:"hello,omitempty"`
	Call   *Call         `json:"call,omitempty"`
	Result *Result       `json:"result,omitempty"`
	Rules  []policy.Rule `json:"rules,omitempty"`
}

type Hello struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
}

// Call asks the agent to run one tool.
type Call struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	// Caller describes who asked (e.g. "claude", "api:n8n"), for the audit log.
	Caller string `json:"caller"`
	// MaxTier caps what may run: "read" for read-only tokens, "" for no cap.
	MaxTier string `json:"max_tier,omitempty"`
	// Approval is set when the user approved this exact action on the hub.
	Approval *Approval `json:"approval,omitempty"`
}

type Approval struct {
	ID         string `json:"id"`
	ActionHash string `json:"action_hash"`
}

// Result is the outcome of a Call.
type Result struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
	// Data is structured output (tool-specific).
	Data json.RawMessage `json:"data,omitempty"`
	// NeedsApproval is set when the action is elevated and was not approved.
	NeedsApproval *NeedsApproval `json:"needs_approval,omitempty"`
}

type NeedsApproval struct {
	ActionHash    string      `json:"action_hash"`
	Summary       string      `json:"summary"`
	Why           string      `json:"why"`
	SuggestedRule policy.Rule `json:"suggested_rule"`
	// AskAlways: this action needs its own approval every time; it must not
	// be approved together with other actions or turned into a rule.
	AskAlways bool `json:"ask_always,omitempty"`
}
