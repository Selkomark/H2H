// Package harness starts harness products. It knows nothing about any one
// product: everything specific to a product comes from its config entry.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Selkomark/H2H/internal/config"
)

// baseEnv is what every harness may see of the user's environment. Anything
// else must be named in the harness's pass_env.
var baseEnv = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TERM": true, "COLORTERM": true, "LANG": true, "TMPDIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
}

var basePrefixes = []string{"LC_", "XDG_"}

// Env builds the environment for a harness process from environ, keeping
// only the basic variables and those the harness is declared to need, then
// adding extra.
func Env(h config.Harness, environ []string, extra ...string) []string {
	allowed := make(map[string]bool, len(h.PassEnv))
	for _, name := range h.PassEnv {
		allowed[name] = true
	}
	var env []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if baseEnv[name] || allowed[name] || hasAnyPrefix(name, basePrefixes) {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// Ask runs the harness once, without a terminal, to answer prompt. The
// caller bounds the run with ctx.
func Ask(ctx context.Context, h config.Harness, environ []string, dir, prompt string, stdout, stderr io.Writer) error {
	if len(h.AskArgs) == 0 {
		return errors.New("this harness has no ask_args in the config, so it cannot answer a prompt")
	}
	args := append(append([]string{}, h.AskArgs...), prompt)
	cmd := exec.CommandContext(ctx, h.Command, args...)
	cmd.Dir = dir
	cmd.Env = Env(h, environ)
	// Stdin stays closed: a harness left with an open input can finish its
	// work and never exit.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// versionWait is how long Version waits for output after the harness exits,
// in case something it started still holds the pipe.
const versionWait = time.Second

// Version runs the harness with its version_args and returns the first
// line it prints. An error means the program is installed but does not run.
func Version(ctx context.Context, h config.Harness, environ []string) (string, error) {
	if len(h.VersionArgs) == 0 {
		return "", errors.New("this harness has no version_args in the config")
	}
	cmd := exec.CommandContext(ctx, h.Command, h.VersionArgs...)
	cmd.Env = Env(h, environ)
	cmd.WaitDelay = versionWait
	out, err := cmd.CombinedOutput()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	line = strings.TrimSpace(line)
	if err != nil {
		if line != "" {
			return "", fmt.Errorf("%w: %s", err, line)
		}
		return "", err
	}
	return line, nil
}

// Latest runs the harness's latest_command and returns the first line it
// prints: the newest version there is to install.
func Latest(ctx context.Context, h config.Harness, environ []string) (string, error) {
	if len(h.LatestCommand) == 0 {
		return "", errors.New("this harness has no latest_command in the config")
	}
	cmd := exec.CommandContext(ctx, h.LatestCommand[0], h.LatestCommand[1:]...)
	// The user's own environment: this is their package manager, which
	// needs its settings, and not the harness.
	cmd.Env = environ
	cmd.WaitDelay = versionWait
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", h.LatestCommand[0], err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line), nil
}

// Install runs the harness's install_command, which installs its latest
// version or updates it. The caller bounds the run with ctx.
func Install(ctx context.Context, h config.Harness, environ []string, stdout, stderr io.Writer) error {
	if len(h.InstallCommand) == 0 {
		return errors.New("this harness has no install_command in the config")
	}
	cmd := exec.CommandContext(ctx, h.InstallCommand[0], h.InstallCommand[1:]...)
	cmd.Env = environ // the user's package manager, as in Latest
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = versionWait
	return cmd.Run()
}

var versionRE = regexp.MustCompile(`\d+(\.\d+)+`)

// Number picks the version number out of text such as "tool 1.2.3 (beta)".
// It returns "" when there is none.
func Number(text string) string { return versionRE.FindString(text) }

// Newer reports whether version a is later than version b, comparing their
// numbers part by part. It is false when either has no number.
func Newer(a, b string) bool {
	pa, pb := strings.Split(Number(a), "."), strings.Split(Number(b), ".")
	if pa[0] == "" || pb[0] == "" {
		return false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// RolesFile is the name of the file a harness reads its role from when it
// is given one through context_env.
const RolesFile = "AGENTS.md"

// Context returns the extra arguments and environment that tell a harness
// its role: roles is the XML, and rolesDir the directory that holds it as
// RolesFile. Both are empty when the harness takes no context.
func Context(h config.Harness, roles, rolesDir string) (args, env []string) {
	for _, a := range h.ContextArgs {
		args = append(args, strings.ReplaceAll(a, config.RolesPlaceholder, roles))
	}
	for _, kv := range h.ContextEnv {
		env = append(env, strings.ReplaceAll(kv, config.RolesDirPlaceholder, rolesDir))
	}
	return args, env
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
