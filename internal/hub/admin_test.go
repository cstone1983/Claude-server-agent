package hub

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

func TestRenameServer(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "http://localhost"}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.store.Update(func(d *stateData) error {
		d.Agents["a1"] = &AgentRec{ID: "a1", Name: "Ubuntu"}
		d.Agents["a2"] = &AgentRec{ID: "a2", Name: "web01"}
		d.Rules = []*HubRule{{ID: "r1", Scope: "server:Ubuntu", Rule: policy.Rule{Argv: []string{"true"}}}, {ID: "r2", Scope: "all"}}
		return nil
	})
	admin := h.adminHandler()
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}
	if rec := do("/servers/Ubuntu/rename", `{"new_name":"web01"}`); rec.Code != 400 {
		t.Fatalf("duplicate name allowed: %d", rec.Code)
	}
	if rec := do("/servers/Ubuntu/rename", `{"new_name":"bad name"}`); rec.Code != 400 {
		t.Fatalf("invalid name allowed: %d", rec.Code)
	}
	if rec := do("/servers/nope/rename", `{"new_name":"x"}`); rec.Code != 400 {
		t.Fatalf("missing server: %d", rec.Code)
	}
	if rec := do("/servers/Ubuntu/rename", `{"new_name":"n8n"}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	h.store.View(func(d *stateData) {
		if d.Agents["a1"].Name != "n8n" || d.Rules[0].Scope != "server:n8n" || d.Rules[1].Scope != "all" {
			t.Fatalf("after rename: %+v %+v %+v", d.Agents["a1"], d.Rules[0], d.Rules[1])
		}
	})
	if targets, err := h.resolveTargets("n8n", nil); err != nil || len(targets) != 1 || targets[0].ID != "a1" {
		t.Fatalf("resolve new name: %v %v", targets, err)
	}
}
