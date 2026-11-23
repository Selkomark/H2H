// Package harness starts harness products. It knows nothing about any one
// product: everything specific to a product comes from its config entry.
package harness

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"

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
