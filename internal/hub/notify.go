package hub

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

var notifyClient = &http.Client{Timeout: 15 * time.Second}

func (h *Hub) notify(ap Approval) {
	n := h.cfg.Notify
	if n.NtfyURL != "" {
		body := ap.Summary
		if ap.Reason != "" {
			body += "\n\nReason: " + ap.Reason
		}
		req, _ := http.NewRequest(http.MethodPost, n.NtfyURL, bytes.NewReader([]byte(truncate(body, 3000))))
		req.Header.Set("Title", "Approval needed on "+ap.Server)
		req.Header.Set("Tags", "warning")
		req.Header.Set("Priority", "high")
		req.Header.Set("Click", ap.URL)
		req.Header.Set("Actions", "view, Review, "+ap.URL)
		if n.NtfyToken != "" {
			req.Header.Set("Authorization", "Bearer "+n.NtfyToken)
		}
		send("ntfy", req)
	}
	if n.WebhookURL != "" {
		b, _ := json.Marshal(map[string]any{
			"event":    "approval_requested",
			"approval": map[string]any{"id": ap.ID, "server": ap.Server, "tool": ap.Tool, "summary": ap.Summary, "why": ap.Why, "reason": ap.Reason, "caller": ap.Caller, "created_at": ap.CreatedAt},
			"url":      ap.URL,
		})
		req, _ := http.NewRequest(http.MethodPost, n.WebhookURL, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		send("webhook", req)
	}
}

func send(kind string, req *http.Request) {
	resp, err := notifyClient.Do(req)
	if err != nil {
		slog.Warn("notification failed", "kind", kind, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("notification rejected", "kind", kind, "status", resp.Status)
	}
}
