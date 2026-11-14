package agent

import (
	"errors"
	"testing"

	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

func TestOutboxKeepsResultsUntilReconnect(t *testing.T) {
	var o outbox
	o.put(proto.Envelope{ID: "c1"}) // offline
	o.attach(func(proto.Envelope) error { return errors.New("broken") })
	o.put(proto.Envelope{ID: "c2"}) // send fails
	var got []string
	o.attach(func(e proto.Envelope) error { got = append(got, e.ID); return nil })
	if len(got) != 2 {
		t.Fatalf("expected both results re-sent, got %v", got)
	}
	o.put(proto.Envelope{ID: "c3"})
	if len(got) != 3 || len(o.items) != 0 {
		t.Fatalf("direct send failed: %v, kept %d", got, len(o.items))
	}
}
