package hub

import (
	"context"
	"testing"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

// A result re-sent by the agent after the connection dropped mid-call still
// reaches the caller.
func TestCallSurvivesReconnect(t *testing.T) {
	h := &Hub{cfg: &Config{CallTimeoutSec: 30}, reg: &registry{conns: map[string]*agentConn{}}}
	c := &agentConn{id: "a1", closed: make(chan struct{})}
	h.reg.conns["a1"] = c
	sent := make(chan string, 1)
	h.testSend = func(_ *agentConn, env proto.Envelope) error { sent <- env.ID; return nil }

	done := make(chan *proto.Result, 1)
	go func() {
		res, err := h.callAgent(context.Background(), "a1", &proto.Call{Tool: "run_command"})
		if err != nil {
			t.Errorf("callAgent: %v", err)
		}
		done <- res
	}()
	id := <-sent
	close(c.closed) // connection drops while the command runs
	time.Sleep(50 * time.Millisecond)
	if !h.reg.deliver(id, &proto.Result{OK: true, Text: "finished"}) { // re-sent on the new connection
		t.Fatal("nobody was waiting for the result")
	}
	select {
	case res := <-done:
		if res == nil || res.Text != "finished" {
			t.Fatalf("got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller never got the result")
	}
}

func TestCallGivesUpAfterGrace(t *testing.T) {
	old := reconnectGrace
	reconnectGrace = 50 * time.Millisecond
	defer func() { reconnectGrace = old }()
	h := &Hub{cfg: &Config{CallTimeoutSec: 30}, reg: &registry{conns: map[string]*agentConn{}}}
	c := &agentConn{id: "a1", closed: make(chan struct{})}
	h.reg.conns["a1"] = c
	h.testSend = func(*agentConn, proto.Envelope) error { close(c.closed); return nil }
	if _, err := h.callAgent(context.Background(), "a1", &proto.Call{}); err == nil {
		t.Fatal("expected an error after the grace period")
	}
}
