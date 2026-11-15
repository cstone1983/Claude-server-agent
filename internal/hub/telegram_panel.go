package hub

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// The hands-off control bar: a persistent button bar (Telegram reply
// keyboard) at the bottom of the chat. While hands-off is off it offers
// 1h / 4h / 8h, an "Include Proxmox" box and Status; while it is on, Stop and
// Status. It follows changes made on the dashboard and the expiry. Only your
// own chat is obeyed (the same check as the approval buttons). Proxmox
// reboots always keep needing their own OK, whatever the box says.

const (
	btnStatus    = "Status"
	boxOff       = "☐ Include Proxmox"
	boxOn        = "☑ Include Proxmox"
	btnStopPrefx = "⏹ Stop hands-off"
)

var handsOffDurations = map[string]time.Duration{"Hands-off 1h": time.Hour, "4h": 4 * time.Hour, "8h": 8 * time.Hour}

func (t *telegram) keyboard(aa *AutoApprove) map[string]any {
	type btn struct {
		Text string `json:"text"`
	}
	var rows [][]btn
	if aa != nil {
		rows = [][]btn{{{btnStopPrefx + " (until " + aa.Until.In(time.Local).Format("15:04") + careLabel(aa.IncludeCareful) + ")"}}, {{btnStatus}}}
	} else {
		t.panelMu.Lock()
		box := boxOff
		if t.includeCareful {
			box = boxOn
		}
		t.panelMu.Unlock()
		rows = [][]btn{{{"Hands-off 1h"}, {"4h"}, {"8h"}}, {{box}, {btnStatus}}}
	}
	return map[string]any{"keyboard": rows, "is_persistent": true, "resize_keyboard": true}
}

func careLabel(c bool) string {
	if c {
		return ", incl. Proxmox"
	}
	return ""
}

// sendPanel posts text with the current bar.
func (t *telegram) sendPanel(ctx context.Context, text string) {
	aa := t.h.autoApprove()
	t.panelMu.Lock()
	t.panelKnown, t.panelActive = true, aa != nil
	t.panelMu.Unlock()
	if err := t.call(ctx, "sendMessage", map[string]any{"chat_id": t.cfg.ChatID, "text": text, "disable_notification": true, "reply_markup": t.keyboard(aa)}, nil); err != nil {
		slog.Warn("telegram: sending the control bar failed", "err", err)
	}
}

func (t *telegram) panelMessage(ctx context.Context, u tgUpdate) {
	m := u.Message
	if m.From == nil || m.From.ID != t.cfg.ChatID || m.Chat.ID != t.cfg.ChatID {
		if m.From != nil {
			t.h.audit.Write(audit.Entry{Event: "telegram_refused", Detail: map[string]any{"from": m.From.ID, "text": truncate(m.Text, 40)}})
		}
		return
	}
	text := strings.TrimSpace(m.Text)
	slog.Info("telegram: control bar", "text", text)
	switch {
	case text == boxOff || text == boxOn:
		t.panelMu.Lock()
		t.includeCareful = text == boxOff
		on := t.includeCareful
		t.panelMu.Unlock()
		if on {
			t.sendPanel(ctx, "Proxmox will be included in the next hands-off (its reboots still need their own OK).")
		} else {
			t.sendPanel(ctx, "Proxmox will not be included.")
		}
	case handsOffDurations[text] > 0:
		t.panelMu.Lock()
		care := t.includeCareful
		t.includeCareful = false // the box is for one use only
		t.panelMu.Unlock()
		n, err := t.h.setAutoApprove(handsOffDurations[text], "all", care, "telegram")
		if err != nil {
			t.sendPanel(ctx, "Could not turn hands-off on: "+err.Error())
			return
		}
		aa := t.h.autoApprove()
		msg := fmt.Sprintf("Hands-off ON until %s for %s.", aa.Until.In(time.Local).Format("15:04"), aa.label())
		if care {
			msg += " Proxmox reboots still need their own OK."
		}
		if n > 0 {
			msg += fmt.Sprintf(" Approved %d waiting request(s).", n)
		}
		t.sendPanel(ctx, msg)
	case strings.HasPrefix(text, btnStopPrefx):
		t.h.clearAutoApprove("telegram")
		t.sendPanel(ctx, "Hands-off OFF. Approvals will ask you again.")
	case text == btnStatus:
		t.sendPanel(ctx, t.statusText())
	default: // /start, anything else: show the bar
		t.sendPanel(ctx, t.statusText())
	}
}

func (t *telegram) statusText() string {
	var b strings.Builder
	if aa := t.h.autoApprove(); aa != nil {
		fmt.Fprintf(&b, "Hands-off ON until %s for %s.", aa.Until.In(time.Local).Format("15:04"), aa.label())
	} else {
		b.WriteString("Hands-off is off.")
	}
	n := 0
	for _, ap := range t.h.appr.list() {
		if ap.Status != StatusPending {
			continue
		}
		n++
		if n <= 5 {
			fmt.Fprintf(&b, "\n• %s: %s", ap.Server, truncate(strings.ReplaceAll(ap.Summary, "\n", " "), 70))
		}
	}
	switch {
	case n == 0:
		b.WriteString("\nNo approvals waiting.")
	case n > 5:
		fmt.Fprintf(&b, "\n(+%d more)", n-5)
	}
	return b.String()
}

// watchPanel keeps the bar in step with hands-off changes made elsewhere
// (dashboard) and with the expiry.
func (t *telegram) watchPanel(ctx context.Context) {
	if !t.cfg.Buttons {
		return
	}
	tk := time.NewTicker(20 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		if !t.h.replica.serving() {
			t.panelMu.Lock()
			t.panelKnown = false // re-learn the state when serving again, without a message
			t.panelMu.Unlock()
			continue
		}
		t.syncPanel(ctx)
	}
}

func (t *telegram) syncPanel(ctx context.Context) {
	aa := t.h.autoApprove()
	t.panelMu.Lock()
	known, was := t.panelKnown, t.panelActive
	if !known {
		t.panelKnown, t.panelActive = true, aa != nil
	}
	t.panelMu.Unlock()
	if !known || was == (aa != nil) {
		return
	}
	if aa != nil {
		t.sendPanel(ctx, fmt.Sprintf("Hands-off ON (set elsewhere) until %s for %s.", aa.Until.In(time.Local).Format("15:04"), aa.label()))
	} else {
		t.sendPanel(ctx, "Hands-off ended. Approvals will ask you again.")
	}
}
