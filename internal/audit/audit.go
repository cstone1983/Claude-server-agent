// Package audit writes an append-only JSON Lines audit log.
package audit

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Entry struct {
	Time       time.Time `json:"time"`
	Event      string    `json:"event"`
	Caller     string    `json:"caller,omitempty"`
	Server     string    `json:"server,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Tier       string    `json:"tier,omitempty"`
	RuleID     string    `json:"rule_id,omitempty"`
	ApprovalID string    `json:"approval_id,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	Detail     any       `json:"detail,omitempty"`
}

type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, f: f}, nil
}

func (l *Log) Path() string { return l.path }

func (l *Log) Write(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		slog.Error("audit marshal", "err", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		slog.Error("audit write", "err", err)
	}
	slog.Info("audit", "event", e.Event, "tool", e.Tool, "tier", e.Tier, "caller", e.Caller, "summary", truncate(e.Summary, 200))
}

// Tail returns the last n entries.
func Tail(path string, n int) ([]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []json.RawMessage
	start := len(b)
	for i := len(b) - 1; i >= 0 && len(lines) < n; i-- {
		if b[i] == '\n' || i == 0 {
			s := i
			if b[i] == '\n' {
				s = i + 1
			}
			if s < start {
				lines = append(lines, json.RawMessage(append([]byte{}, b[s:start]...)))
			}
			start = i
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
