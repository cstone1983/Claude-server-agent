// Command csa-hub is the Claude Server Agent hub: the single public endpoint
// that Claude connects to and that every csa-agent dials out to.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/cstone1983/Claude-server-agent/internal/cli"
	"github.com/cstone1983/Claude-server-agent/internal/hub"
	"github.com/cstone1983/Claude-server-agent/internal/totp"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

const usage = `csa-hub — Claude Server Agent hub

Setup:
  csa-hub init --public-url https://agents.example.com   create config, admin password and authenticator secret
  csa-hub serve                                          run the hub (normally via systemd)

Servers:
  csa-hub join-token [--ttl 60] [--uses 1] [--tags a,b] create a token for enrolling a server
  csa-hub servers                                        list servers
  csa-hub rename <server> <new-name>                     give a server a friendly name
  csa-hub tag <server> <tag,tag>                         set a server's tags
  csa-hub remove-server <server>                         remove a server

Approvals and rules:
  csa-hub approvals [--all]                              list pending approvals
  csa-hub approve <id> [--always] [--scope server|tag:x|all]
  csa-hub deny <id>
  csa-hub rules                                          list "always allow" rules
  csa-hub rule-delete <rule-id>

Access:
  csa-hub token create <name> [--scope read|full] [--servers web01,tag:web]   API token for n8n etc.
  csa-hub token list | token revoke <name>
  csa-hub sessions | revoke-sessions                     Claude connector logins, dashboard logins, remembered devices
  csa-hub set-password | reset-totp

Other:
  csa-hub call <tool> '<json args>'                      run a tool from the shell (as admin)
  csa-hub version

Global flag: --config (default /etc/csa-hub/hub.yaml)
`

var configPath = hub.DefaultConfigPath

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	// Allow --config anywhere.
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" || args[i] == "-config" {
			if i+1 < len(args) {
				configPath = args[i+1]
				args = append(args[:i], args[i+2:]...)
				i--
			}
		} else if v, ok := strings.CutPrefix(args[i], "--config="); ok {
			configPath = v
			args = append(args[:i], args[i+1:]...)
			i--
		}
	}
	switch cmd {
	case "serve":
		serve()
	case "init":
		initCmd(args)
	case "join-token":
		joinToken(args)
	case "servers":
		printJSON(admin("GET", "/servers", nil))
	case "tag":
		if len(args) != 2 {
			cli.Fatal("usage: csa-hub tag <server> <tag,tag>")
		}
		printJSON(admin("POST", "/servers/"+args[0]+"/tags", map[string]any{"tags": cli.SplitList(args[1])}))
	case "rename":
		if len(args) != 2 {
			cli.Fatal("usage: csa-hub rename <current-name> <new-name>")
		}
		printJSON(admin("POST", "/servers/"+args[0]+"/rename", map[string]any{"new_name": args[1]}))
	case "remove-server":
		if len(args) != 1 {
			cli.Fatal("usage: csa-hub remove-server <server>")
		}
		printJSON(admin("DELETE", "/servers/"+args[0], nil))
	case "approvals":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		all := fs.Bool("all", false, "include decided approvals")
		cli.Parse(fs, args)
		q := ""
		if *all {
			q = "?all=1"
		}
		printApprovals(admin("GET", "/approvals"+q, nil))
	case "approve", "deny":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		always := fs.Bool("always", false, "also add an allow rule")
		scope := fs.String("scope", "server", "rule scope: server, tag:<tag> or all")
		pos := cli.Parse(fs, args)
		if len(pos) != 1 {
			cli.Fatal("usage: csa-hub %s <approval-id>", cmd)
		}
		printJSON(admin("POST", "/approvals/"+pos[0], map[string]any{"decision": cmd, "always": *always, "scope": *scope}))
	case "rules":
		printJSON(admin("GET", "/rules", nil))
	case "rule-delete":
		if len(args) != 1 {
			cli.Fatal("usage: csa-hub rule-delete <rule-id>")
		}
		printJSON(admin("DELETE", "/rules/"+args[0], nil))
	case "token":
		tokenCmd(args)
	case "sessions":
		printJSON(admin("GET", "/sessions", nil))
	case "revoke-sessions":
		printJSON(admin("DELETE", "/sessions", nil))
	case "set-password":
		setPassword()
	case "reset-totp":
		resetTOTP()
	case "call":
		if len(args) < 1 {
			cli.Fatal("usage: csa-hub call <tool> '<json>'")
		}
		body := json.RawMessage("{}")
		if len(args) > 1 {
			body = json.RawMessage(args[1])
		}
		var out struct {
			Text    string `json:"text"`
			IsError bool   `json:"is_error"`
		}
		json.Unmarshal(admin("POST", "/tools/"+args[0], body), &out)
		fmt.Println(out.Text)
		if out.IsError {
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("csa-hub", version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func serve() {
	cfg, err := hub.LoadConfig(configPath)
	if err != nil {
		cli.Fatal("%v", err)
	}
	h, err := hub.New(cfg)
	if err != nil {
		cli.Fatal("%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := h.Serve(ctx); err != nil {
		slog.Error("hub stopped", "err", err)
		os.Exit(1)
	}
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	publicURL := fs.String("public-url", "", "public https URL of the hub, e.g. https://agents.example.com")
	listen := fs.String("listen", "127.0.0.1:7420", "local listen address (cloudflared forwards here)")
	password := fs.String("password", "", "admin password (default: $CSA_ADMIN_PASSWORD, else generate one)")
	force := fs.Bool("force", false, "overwrite an existing config")
	cli.Parse(fs, args)
	if *publicURL == "" {
		cli.Fatal("--public-url is required")
	}
	if !strings.HasPrefix(*publicURL, "https://") && !strings.HasPrefix(*publicURL, "http://localhost") && !strings.HasPrefix(*publicURL, "http://127.0.0.1") {
		cli.Fatal("--public-url must be https:// (Claude requires HTTPS)")
	}
	if _, err := os.Stat(configPath); err == nil && !*force {
		cli.Fatal("%s already exists (use --force to overwrite, or set-password / reset-totp)", configPath)
	}
	pw := *password
	if pw == "" {
		pw = os.Getenv("CSA_ADMIN_PASSWORD")
	}
	chosen := pw != ""
	if chosen && len(pw) < 10 {
		cli.Fatal("the admin password must be at least 10 characters")
	}
	if pw == "" {
		pw = genPassword()
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		cli.Fatal("%v", err)
	}
	cfg := &hub.Config{PublicURL: strings.TrimRight(*publicURL, "/"), Listen: *listen, AdminPasswordHash: string(hash), TOTPSecret: totp.NewSecret()}
	if err := hub.SaveConfig(configPath, cfg); err != nil {
		cli.Fatal("%v", err)
	}
	fmt.Printf("Wrote %s\n\n", configPath)
	if chosen {
		fmt.Println("Admin password: (the one you chose)")
	} else {
		fmt.Printf("Admin password: %s\n", pw)
	}
	fmt.Println("(Used to connect Claude and to log in to the approvals dashboard. Change it any time with `sudo csa-hub set-password`.)")
	fmt.Println()
	showTOTP(cfg.TOTPSecret, cfg.PublicURL)
}

func showTOTP(secret, publicURL string) {
	host := strings.TrimPrefix(strings.TrimPrefix(publicURL, "https://"), "http://")
	uri := totp.URI(secret, "Claude Server Agent", host)
	fmt.Println("Scan this with your authenticator app (Google Authenticator, 1Password, Authy, ...):")
	qrterminal.GenerateHalfBlock(uri, qrterminal.L, os.Stdout)
	fmt.Printf("Or enter the secret manually: %s\n", secret)
	fmt.Println("You'll need a code from it to log in and to approve actions.")
}

func genPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	f, _ := os.Open("/dev/urandom")
	io.ReadFull(f, b)
	f.Close()
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(chars[int(c)%len(chars)])
	}
	return sb.String()
}

func setPassword() {
	cfg, err := hub.LoadConfig(configPath)
	if err != nil {
		cli.Fatal("%v", err)
	}
	pw := readNewPassword()
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
	cfg.AdminPasswordHash = string(hash)
	if err := hub.SaveConfig(configPath, cfg); err != nil {
		cli.Fatal("%v", err)
	}
	fmt.Println("Password updated.")
	restartHub()
}

// readNewPassword asks for a password twice on a terminal (input hidden), or
// reads one line from stdin when piped.
func readNewPassword() string {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimRight(pw, "\r\n")
		if len(pw) < 10 {
			cli.Fatal("use at least 10 characters")
		}
		return pw
	}
	for {
		fmt.Print("New admin password (at least 10 characters): ")
		a, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if len(a) < 10 {
			fmt.Println("Too short, try again.")
			continue
		}
		fmt.Print("Repeat it: ")
		b, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if string(a) != string(b) {
			fmt.Println("They don't match, try again.")
			continue
		}
		return string(a)
	}
}

// restartHub restarts the systemd service so a config change takes effect.
func restartHub() {
	if os.Geteuid() != 0 {
		fmt.Println("Restart the hub to apply it: sudo systemctl restart csa-hub")
		return
	}
	if out, err := exec.Command("systemctl", "try-restart", "csa-hub").CombinedOutput(); err != nil {
		fmt.Printf("Could not restart csa-hub (%v): %s\nRestart it manually: systemctl restart csa-hub\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("Hub restarted; the new setting is active.")
}

func resetTOTP() {
	cfg, err := hub.LoadConfig(configPath)
	if err != nil {
		cli.Fatal("%v", err)
	}
	cfg.TOTPSecret = totp.NewSecret()
	if err := hub.SaveConfig(configPath, cfg); err != nil {
		cli.Fatal("%v", err)
	}
	showTOTP(cfg.TOTPSecret, cfg.PublicURL)
	fmt.Println()
	restartHub()
}

func joinToken(args []string) {
	fs := flag.NewFlagSet("join-token", flag.ExitOnError)
	ttl := fs.Int("ttl", 60, "minutes until the token expires")
	uses := fs.Int("uses", 1, "how many servers may use it")
	tags := fs.String("tags", "", "tags added to servers that join with it")
	quiet := fs.Bool("quiet", false, "print only the token")
	cli.Parse(fs, args)
	var out struct {
		Token  string    `json:"token"`
		HubURL string    `json:"hub_url"`
		Exp    time.Time `json:"expires_at"`
	}
	json.Unmarshal(admin("POST", "/join-tokens", map[string]any{"ttl_minutes": *ttl, "uses": *uses, "tags": cli.SplitList(*tags)}), &out)
	if *quiet {
		fmt.Println(out.Token)
		return
	}
	fmt.Printf("Join token (valid until %s, %d use(s)):\n  %s\n\n", out.Exp.Local().Format(time.RFC1123), *uses, out.Token)
	fmt.Println("On each server, paste this as ONE line (it installs curl first if missing, e.g. on Ubuntu Desktop):")
	fmt.Printf("\n  %s\n", hub.InstallCommand(out.HubURL, out.Token))
	fmt.Println("\nThe installer asks for a friendly name and tags (or add --name NAME --tags a,b).")
}

func tokenCmd(args []string) {
	if len(args) == 0 {
		cli.Fatal("usage: csa-hub token create|list|revoke ...")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("token create", flag.ExitOnError)
		scope := fs.String("scope", "read", "read (read-only actions) or full (same as Claude, approvals included)")
		servers := fs.String("servers", "", "restrict to these servers/tags, comma-separated (default: all)")
		pos := cli.Parse(fs, args[1:])
		if len(pos) != 1 {
			cli.Fatal("usage: csa-hub token create <name> [--scope read|full] [--servers a,tag:b]")
		}
		var out map[string]any
		json.Unmarshal(admin("POST", "/api-tokens", map[string]any{"name": pos[0], "scope": *scope, "servers": cli.SplitList(*servers)}), &out)
		fmt.Printf("API token %q (scope %s). It is shown only once:\n\n  %s\n\n", pos[0], *scope, out["token"])
		fmt.Println("Use it as:  Authorization: Bearer <token>")
		fmt.Println("REST:       POST <hub>/api/v1/tools/<tool>  with a JSON body, e.g. {\"server\":\"web01\"}")
		fmt.Println("MCP:        <hub>/mcp  (for MCP clients that accept a bearer token)")
	case "list":
		printJSON(admin("GET", "/api-tokens", nil))
	case "revoke":
		if len(args) != 2 {
			cli.Fatal("usage: csa-hub token revoke <name>")
		}
		printJSON(admin("DELETE", "/api-tokens/"+args[1], nil))
	default:
		cli.Fatal("unknown token subcommand %q", args[0])
	}
}

func adminSocket() string {
	if cfg, err := hub.LoadConfig(configPath); err == nil {
		return cfg.AdminSocket
	}
	return "/run/csa-hub/admin.sock"
}

func admin(method, path string, body any) []byte {
	sock := adminSocket()
	c := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://hub"+path, rd)
	resp, err := c.Do(req)
	if err != nil {
		cli.Fatal("cannot reach the hub admin socket %s (is csa-hub running, and are you root?): %v", sock, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		cli.Fatal("%s", strings.TrimSpace(string(b)))
	}
	return b
}

func printJSON(b []byte) {
	var v any
	if json.Unmarshal(b, &v) != nil {
		os.Stdout.Write(b)
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func printApprovals(b []byte) {
	var list []hub.Approval
	json.Unmarshal(b, &list)
	if len(list) == 0 {
		fmt.Println("No pending approvals.")
		return
	}
	for _, a := range list {
		fmt.Printf("%s  [%s]  %s  %s\n    %s\n    why: %s\n    rule if always: %s\n\n", a.ID, a.Status, a.Server, a.CreatedAt.Local().Format("2006-01-02 15:04"), strings.ReplaceAll(a.Summary, "\n", "\n    "), a.Why, a.Suggested.String())
	}
}
