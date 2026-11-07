package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

const (
	pongWait   = 90 * time.Second
	writeWait  = 15 * time.Second
	maxMessage = 16 << 20
)

type Agent struct {
	cfg    *Config
	key    ed25519.PrivateKey
	policy *policy.Policy
	exec   *Executor
	audit  *audit.Log
}

func New(cfg *Config) (*Agent, error) {
	key, err := LoadKey(cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	p, err := policy.Load(cfg.PolicyFile, cfg.HubRulesFile)
	if err != nil {
		return nil, err
	}
	al, err := audit.Open(cfg.AuditLog)
	if err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, key: key, policy: p, audit: al, exec: NewExecutor(cfg, p, al)}, nil
}

func (a *Agent) Policy() *policy.Policy { return a.policy }

// Run keeps a connection to the hub open until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.audit.Write(audit.Entry{Event: "agent_started", Detail: map[string]string{"version": version.String(), "hub": a.cfg.HubURL}})
	backoff := time.Second
	for {
		start := time.Now()
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("hub connection lost", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func wsURL(hub string) string {
	switch {
	case strings.HasPrefix(hub, "https://"):
		return "wss://" + strings.TrimPrefix(hub, "https://") + "/agent/connect"
	case strings.HasPrefix(hub, "http://"):
		return "ws://" + strings.TrimPrefix(hub, "http://") + "/agent/connect"
	}
	return hub + "/agent/connect"
}

func (a *Agent) connectOnce(ctx context.Context) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(a.key, proto.SigningPayload(a.cfg.AgentID, ts))
	h := http.Header{}
	h.Set(proto.HeaderAgentID, a.cfg.AgentID)
	h.Set(proto.HeaderTimestamp, ts)
	h.Set(proto.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	h.Set("User-Agent", "csa-agent/"+version.Version)
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.DialContext(ctx, wsURL(a.cfg.HubURL), h)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %w (HTTP %s)", err, resp.Status)
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	slog.Info("connected to hub", "hub", a.cfg.HubURL, "agent", a.cfg.Name)

	conn.SetReadLimit(maxMessage)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPingHandler(func(data string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeWait))
	})

	var wmu sync.Mutex
	send := func(env proto.Envelope) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(writeWait))
		return conn.WriteJSON(env)
	}
	host, _ := os.Hostname()
	if err := send(proto.Envelope{Type: proto.TypeHello, Hello: &proto.Hello{
		Hostname: host, OS: osPrettyName(), Kernel: kernelRelease(), Arch: runtime.GOARCH, AgentVersion: version.String(),
	}}); err != nil {
		return err
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		conn.Close()
	}()

	for {
		var env proto.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(pongWait))
		switch env.Type {
		case proto.TypeCall:
			if env.Call == nil {
				continue
			}
			go func(id string, call *proto.Call) {
				res := a.exec.Handle(connCtx, call)
				if err := send(proto.Envelope{Type: proto.TypeResult, ID: id, Result: res}); err != nil {
					slog.Error("sending result", "err", err)
				}
			}(env.ID, env.Call)
		case proto.TypeSetRules:
			if err := a.policy.SetHubRules(env.Rules); err != nil {
				slog.Error("saving hub rules", "err", err)
			} else {
				b, _ := json.Marshal(env.Rules)
				a.audit.Write(audit.Entry{Event: "hub_rules_updated", Detail: json.RawMessage(b)})
			}
		}
	}
}

// RunLocal runs one call without the hub (used by `csa-agent exec` for testing).
func (a *Agent) RunLocal(ctx context.Context, call *proto.Call) *proto.Result {
	return a.exec.Handle(ctx, call)
}
