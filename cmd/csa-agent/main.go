// Command csa-agent runs on each server. It dials out to the hub, enforces
// this server's policy and runs the tools Claude asks for.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cstone1983/Claude-server-agent/internal/agent"
	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/cli"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

const usage = `csa-agent — Claude Server Agent (runs on each server)

  csa-agent enroll --hub https://agents.example.com --join <token> [--name web01] [--tags web,prod] [--replace]
  csa-agent run                            connect to the hub (normally via systemd)
  csa-agent check -- <command> [args...]   show which tier a command falls into
  csa-agent check --shell '<script>'
  csa-agent check --read <path> | --write <path>
  csa-agent call <tool> '<json args>'      run a tool locally through the policy (no approvals)
  csa-agent policy                         show the effective policy
  csa-agent audit [-n 50]                  show recent audit entries
  csa-agent version

Global flag: --config (default /etc/csa/agent.yaml)
`

var configPath = agent.DefaultConfigPath

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if (args[i] == "--config" || args[i] == "-config") && i+1 < len(args) {
			configPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			i--
		} else if v, ok := strings.CutPrefix(args[i], "--config="); ok {
			configPath = v
			args = append(args[:i], args[i+1:]...)
			i--
		}
	}
	switch cmd {
	case "enroll":
		fs := flag.NewFlagSet("enroll", flag.ExitOnError)
		hubURL := fs.String("hub", "", "hub URL, e.g. https://agents.example.com")
		join := fs.String("join", "", "join token from `csa-hub join-token`")
		name := fs.String("name", "", "server name shown to Claude (default: hostname)")
		tags := fs.String("tags", "", "comma-separated tags, e.g. web,prod")
		replace := fs.Bool("replace", false, "replace an existing server with the same name")
		cli.Parse(fs, args)
		if *hubURL == "" || *join == "" {
			cli.Fatal("--hub and --join are required")
		}
		if *name == "" {
			h, _ := os.Hostname()
			*name = strings.Split(h, ".")[0]
		}
		cfg, err := agent.Enroll(configPath, *hubURL, *join, *name, cli.SplitList(*tags), *replace)
		if err != nil {
			cli.Fatal("%v", err)
		}
		fmt.Printf("Enrolled as %q (%s) with hub %s\n", cfg.Name, cfg.AgentID, cfg.HubURL)
	case "run":
		cfg, err := agent.LoadConfig(configPath)
		if err != nil {
			cli.Fatal("%v", err)
		}
		a, err := agent.New(cfg)
		if err != nil {
			cli.Fatal("%v", err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				if err := a.Policy().Reload(); err != nil {
					slog.Error("policy reload failed", "err", err)
				} else {
					slog.Info("policy reloaded")
				}
			}
		}()
		if err := a.Run(ctx); err != nil {
			cli.Fatal("%v", err)
		}
	case "check":
		check(args)
	case "call":
		if len(args) < 1 {
			cli.Fatal("usage: csa-agent call <tool> '<json>'")
		}
		cfg, err := agent.LoadConfig(configPath)
		if err != nil {
			cli.Fatal("%v", err)
		}
		a, err := agent.New(cfg)
		if err != nil {
			cli.Fatal("%v", err)
		}
		body := json.RawMessage("{}")
		if len(args) > 1 {
			body = json.RawMessage(args[1])
		}
		res := a.RunLocal(context.Background(), &proto.Call{Tool: args[0], Args: body, Caller: "local-cli"})
		switch {
		case res.NeedsApproval != nil:
			fmt.Printf("Needs approval (%s): %s\nAllow rule that would match:\n  %s\n", res.NeedsApproval.Why, res.NeedsApproval.Summary, res.NeedsApproval.SuggestedRule.String())
			os.Exit(3)
		case res.Error != "":
			cli.Fatal("%s", res.Error)
		default:
			fmt.Println(res.Text)
		}
	case "policy":
		p := loadPolicy()
		b, _ := json.MarshalIndent(p.Snapshot(), "", "  ")
		fmt.Println(string(b))
	case "audit":
		fs := flag.NewFlagSet("audit", flag.ExitOnError)
		n := fs.Int("n", 50, "entries")
		cli.Parse(fs, args)
		path := "/var/log/csa/audit.jsonl"
		if cfg, err := agent.LoadConfig(configPath); err == nil {
			path = cfg.AuditLog
		}
		lines, err := audit.Tail(path, *n)
		if err != nil {
			cli.Fatal("%v", err)
		}
		for _, l := range lines {
			fmt.Println(string(l))
		}
	case "version", "--version", "-v":
		fmt.Println("csa-agent", version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func loadPolicy() *policy.Policy {
	pf, hf := "/etc/csa/policy.yaml", "/var/lib/csa/hub-rules.yaml"
	if cfg, err := agent.LoadConfig(configPath); err == nil {
		pf, hf = cfg.PolicyFile, cfg.HubRulesFile
	}
	p, err := policy.Load(pf, hf)
	if err != nil {
		cli.Fatal("%v", err)
	}
	return p
}

func check(args []string) {
	var act policy.Action
	switch {
	case len(args) >= 2 && args[0] == "--shell":
		act = policy.Action{Kind: "shell", Shell: args[1]}
	case len(args) >= 2 && args[0] == "--read":
		act = policy.Action{Kind: "read_file", Path: args[1]}
	case len(args) >= 2 && args[0] == "--write":
		act = policy.Action{Kind: "write_file", Path: args[1]}
	default:
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			cli.Fatal("usage: csa-agent check -- <command> [args...]")
		}
		act = policy.Action{Kind: "exec", Argv: args}
	}
	d := loadPolicy().Classify(act)
	fmt.Printf("tier:   %s\nreason: %s\n", d.Tier, d.Why)
	if d.RuleID != "" {
		fmt.Printf("rule:   %s\n", d.RuleID)
	}
	if d.Tier == policy.TierElevated {
		r := policy.SuggestRule(act)
		fmt.Printf("\nTo allow it without approval, add to /etc/csa/policy.yaml under allow:\n")
		b, _ := json.Marshal(r)
		fmt.Printf("  - %s\n", string(b))
	}
}
