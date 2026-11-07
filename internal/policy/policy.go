// Package policy classifies actions into tiers: read (runs freely), allowed
// (matches an allowlist rule, runs freely), elevated (needs the user's
// approval) and denied (never runs).
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	TierRead     = "read"
	TierAllowed  = "allowed"
	TierElevated = "elevated"
	TierDenied   = "denied"
)

// Rule is one allow/deny/read entry. Only one of Argv, Shell, WritePaths or
// ReadPaths is normally set.
type Rule struct {
	ID      string `yaml:"id,omitempty" json:"id,omitempty"`
	Comment string `yaml:"comment,omitempty" json:"comment,omitempty"`

	// Argv matches a command element by element. See matchArg for syntax; a
	// final "**" element matches any remaining arguments (including none).
	Argv []string `yaml:"argv,omitempty" json:"argv,omitempty"`
	// AnyArg: at least one argument (excluding argv[0]) must match one of these.
	AnyArg []string `yaml:"any_arg,omitempty" json:"any_arg,omitempty"`
	// NotArgs: the rule does not match if any argument matches one of these.
	NotArgs []string `yaml:"not_args,omitempty" json:"not_args,omitempty"`
	// As is the user the command may run as: "" means root, "*" means any.
	As string `yaml:"as,omitempty" json:"as,omitempty"`

	// Shell matches a run_shell script exactly (after trimming whitespace).
	// In deny rules it matches if the script contains the string.
	Shell string `yaml:"shell,omitempty" json:"shell,omitempty"`

	WritePaths []string `yaml:"write_paths,omitempty" json:"write_paths,omitempty"`
	ReadPaths  []string `yaml:"read_paths,omitempty" json:"read_paths,omitempty"`
}

func (r Rule) String() string {
	switch {
	case len(r.Argv) > 0:
		s := "argv " + strings.Join(r.Argv, " ")
		if r.As != "" {
			s += " (as " + r.As + ")"
		}
		return s
	case r.Shell != "":
		return "shell " + r.Shell
	case len(r.WritePaths) > 0:
		return "write " + strings.Join(r.WritePaths, ", ")
	case len(r.ReadPaths) > 0:
		return "read " + strings.Join(r.ReadPaths, ", ")
	}
	return "(empty rule)"
}

// File is the on-disk policy (/etc/csa/policy.yaml). It is edited by the
// server's owner only; the hub can never change it.
type File struct {
	// Read adds commands to the read tier (run freely, any user).
	Read  []Rule `yaml:"read,omitempty"`
	Allow []Rule `yaml:"allow,omitempty"`
	Deny  []Rule `yaml:"deny,omitempty"`
	// SensitivePaths are read_file paths that need approval.
	SensitivePaths []string `yaml:"sensitive_paths,omitempty"`
	// AcceptHubRules controls whether "always allow" rules pushed from the
	// hub are honored. Default true.
	AcceptHubRules *bool `yaml:"accept_hub_rules,omitempty"`
	// DisableBuiltinRead turns off the built-in read-only command list.
	DisableBuiltinRead bool `yaml:"disable_builtin_read,omitempty"`
	// DisableRemoteUpdate stops update_server from installing updates on this
	// server (checking is still allowed).
	DisableRemoteUpdate bool `yaml:"disable_remote_update,omitempty"`
}

// Action is something the agent has been asked to do.
type Action struct {
	Kind  string   // "exec", "shell", "read_file", "write_file"
	Argv  []string // exec
	Shell string   // shell
	As    string   // user to run as; "" = root
	Path  string   // read/write: resolved absolute path
	// OrigPath is the path as requested (before symlink resolution).
	OrigPath string
}

func (a Action) Summary() string {
	who := ""
	if a.As != "" && a.As != "root" {
		who = " (as " + a.As + ")"
	}
	switch a.Kind {
	case "exec":
		return "run: " + quoteArgv(a.Argv) + who
	case "shell":
		return "shell script" + who + ":\n" + a.Shell
	case "read_file":
		return "read file " + a.Path
	case "write_file":
		return "write file " + a.Path
	}
	return a.Kind
}

type Decision struct {
	Tier   string `json:"tier"`
	RuleID string `json:"rule_id,omitempty"`
	Why    string `json:"why"`
}

// Policy combines built-in rules, the local file and hub-pushed rules.
type Policy struct {
	mu       sync.RWMutex
	path     string
	hubPath  string
	local    File
	hubRules []Rule
}

// Load reads the local policy and the hub-managed rules file. Missing files
// are treated as empty.
func Load(path, hubPath string) (*Policy, error) {
	p := &Policy{path: path, hubPath: hubPath}
	return p, p.Reload()
}

// New builds a policy from in-memory values (used by tests and `policy check`).
func New(local File, hub []Rule) *Policy { return &Policy{local: local, hubRules: hub} }

func (p *Policy) Reload() error {
	var f File
	if p.path != "" {
		if err := readYAML(p.path, &f); err != nil {
			return err
		}
	}
	var hub []Rule
	if p.hubPath != "" {
		if err := readYAML(p.hubPath, &hub); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.local, p.hubRules = f, hub
	p.mu.Unlock()
	return nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// SetHubRules replaces the hub-managed rules and persists them.
func (p *Policy) SetHubRules(rules []Rule) error {
	p.mu.Lock()
	p.hubRules = rules
	path := p.hubPath
	p.mu.Unlock()
	if path == "" {
		return nil
	}
	b, err := yaml.Marshal(rules)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	hdr := []byte("# Managed by the csa hub (\"approve and always allow\"). Do not edit;\n# put your own rules in policy.yaml instead.\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(hdr, b...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Snapshot returns the effective rules for display.
func (p *Policy) Snapshot() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]any{
		"local":             p.local,
		"hub_rules":         p.hubRules,
		"builtin_read":      builtinRead,
		"builtin_deny":      builtinDeny,
		"builtin_sensitive": builtinSensitive,
	}
}

// RemoteUpdateAllowed reports whether update_server may install updates.
func (p *Policy) RemoteUpdateAllowed() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.local.DisableRemoteUpdate
}

func (p *Policy) acceptHub() bool {
	return p.local.AcceptHubRules == nil || *p.local.AcceptHubRules
}

// Classify decides the tier for an action.
func (p *Policy) Classify(a Action) Decision {
	p.mu.RLock()
	defer p.mu.RUnlock()

	deny := append(append([]Rule{}, builtinDeny...), p.local.Deny...)
	allow := append([]Rule{}, p.local.Allow...)
	if p.acceptHub() {
		allow = append(allow, p.hubRules...)
	}

	switch a.Kind {
	case "exec":
		argv := normalizeArgv(a.Argv)
		if len(argv) == 0 {
			return Decision{Tier: TierDenied, Why: "empty command"}
		}
		for _, r := range deny {
			if r.matchesExec(argv, a.As, true) {
				return Decision{Tier: TierDenied, RuleID: r.ID, Why: "matches deny rule: " + r.String()}
			}
		}
		read := p.local.Read
		if !p.local.DisableBuiltinRead {
			read = append(append([]Rule{}, builtinRead...), read...)
		}
		for _, r := range read {
			if r.matchesExec(argv, a.As, true) {
				return Decision{Tier: TierRead, RuleID: r.ID, Why: "read-only command"}
			}
		}
		for _, r := range allow {
			if r.matchesExec(argv, a.As, false) {
				return Decision{Tier: TierAllowed, RuleID: r.ID, Why: "matches allow rule: " + r.String()}
			}
		}
		return Decision{Tier: TierElevated, Why: "not read-only and no allow rule matches"}

	case "shell":
		script := strings.TrimSpace(a.Shell)
		for _, r := range deny {
			if r.Shell != "" && strings.Contains(script, r.Shell) {
				return Decision{Tier: TierDenied, RuleID: r.ID, Why: "matches deny rule: " + r.String()}
			}
			if len(r.AnyArg) > 0 && len(r.Argv) == 0 && containsAnyLiteral(script, r.AnyArg) {
				return Decision{Tier: TierDenied, RuleID: r.ID, Why: "script contains a denied argument"}
			}
		}
		for _, r := range allow {
			if r.Shell != "" && strings.TrimSpace(r.Shell) == script && userOK(r.As, a.As) {
				return Decision{Tier: TierAllowed, RuleID: r.ID, Why: "matches allow rule (exact script)"}
			}
		}
		return Decision{Tier: TierElevated, Why: "shell scripts need approval unless allowlisted exactly"}

	case "write_file":
		for _, r := range deny {
			if pat, ok := matchAnyPath(r.WritePaths, a.Path, a.OrigPath); ok {
				return Decision{Tier: TierDenied, RuleID: r.ID, Why: "path matches denied write path " + pat}
			}
		}
		for _, r := range allow {
			if pat, ok := matchAnyPath(r.WritePaths, a.Path); ok {
				return Decision{Tier: TierAllowed, RuleID: r.ID, Why: "path matches allowed write path " + pat}
			}
		}
		return Decision{Tier: TierElevated, Why: "file writes need approval unless the path is allowlisted"}

	case "read_file":
		for _, r := range deny {
			if pat, ok := matchAnyPath(r.ReadPaths, a.Path, a.OrigPath); ok {
				return Decision{Tier: TierDenied, RuleID: r.ID, Why: "path matches denied read path " + pat}
			}
		}
		sensitive := append(append([]string{}, builtinSensitive...), p.local.SensitivePaths...)
		if pat, ok := matchAnyPath(sensitive, a.Path, a.OrigPath); ok {
			for _, r := range allow {
				if _, ok := matchAnyPath(r.ReadPaths, a.Path); ok {
					return Decision{Tier: TierAllowed, RuleID: r.ID, Why: "sensitive path allowlisted"}
				}
			}
			return Decision{Tier: TierElevated, Why: "sensitive path (" + pat + ") needs approval"}
		}
		return Decision{Tier: TierRead, Why: "file read"}
	}
	return Decision{Tier: TierDenied, Why: "unknown action kind " + a.Kind}
}

func containsAnyLiteral(s string, pats []string) bool {
	for _, p := range pats {
		if !strings.ContainsAny(p, "*?|") && strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func userOK(ruleAs, as string) bool {
	if as == "" {
		as = "root"
	}
	if ruleAs == "*" {
		return true
	}
	if ruleAs == "" {
		ruleAs = "root"
	}
	return ruleAs == as
}

// matchesExec reports whether an exec action matches the rule. anyUser lets
// read/deny rules apply regardless of the target user.
func (r Rule) matchesExec(argv []string, as string, anyUser bool) bool {
	if len(r.Argv) == 0 && len(r.AnyArg) == 0 {
		return false
	}
	if !anyUser && !userOK(r.As, as) {
		return false
	}
	if len(r.Argv) > 0 && !matchArgvPattern(r.Argv, argv) {
		return false
	}
	args := argv[1:]
	if len(r.AnyArg) > 0 {
		found := false
		for _, a := range args {
			for _, p := range r.AnyArg {
				if matchArg(p, a) {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	for _, a := range args {
		for _, p := range r.NotArgs {
			if matchArg(p, a) {
				return false
			}
		}
	}
	return true
}

func matchArgvPattern(pat, argv []string) bool {
	for i, p := range pat {
		if p == "**" && i == len(pat)-1 {
			return true
		}
		if i >= len(argv) || !matchArg(p, argv[i]) {
			return false
		}
	}
	return len(argv) == len(pat)
}

var standardBinDirs = map[string]bool{
	"/usr/local/sbin": true, "/usr/local/bin": true, "/usr/sbin": true,
	"/usr/bin": true, "/sbin": true, "/bin": true,
}

// normalizeArgv reduces argv[0] to its base name when it lives in a standard
// system bin directory, so "/usr/bin/df" matches rules written as "df".
func normalizeArgv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := append([]string{}, argv...)
	if strings.Contains(out[0], "/") {
		dir := filepath.Dir(filepath.Clean(out[0]))
		if standardBinDirs[dir] {
			out[0] = filepath.Base(out[0])
		}
	}
	return out
}

// SuggestRule returns the narrowest rule that would allow this action.
func SuggestRule(a Action) Rule {
	r := Rule{}
	if a.As != "" && a.As != "root" {
		r.As = a.As
	}
	switch a.Kind {
	case "exec":
		for _, arg := range normalizeArgv(a.Argv) {
			r.Argv = append(r.Argv, EscapeArg(arg))
		}
	case "shell":
		r.Shell = strings.TrimSpace(a.Shell)
	case "write_file":
		r.WritePaths = []string{escapePath(a.Path)}
	case "read_file":
		r.ReadPaths = []string{escapePath(a.Path)}
	}
	return r
}

// EscapeArg makes a literal argument safe to use as a pattern element.
func EscapeArg(arg string) string {
	if strings.ContainsAny(arg, "*?|") || strings.HasPrefix(arg, "=") {
		return "=" + arg
	}
	return arg
}

func escapePath(p string) string {
	// Path globs have no escape syntax; replace wildcard chars with "?" so the
	// rule stays at least as narrow as a single character.
	return strings.NewReplacer("*", "?").Replace(p)
}

func quoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`;&|<>(){}*?[]#~") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
