package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/harness"
)

const (
	// updateFlag makes install look for newer versions of what is installed.
	updateFlag = "--update"
	// latestTimeout bounds asking for the latest version of one harness.
	latestTimeout = 30 * time.Second
	// installTimeout bounds installing one harness.
	installTimeout = 10 * time.Minute
)

// installJob is one harness the user may choose to install or update.
type installJob struct {
	name string
	h    config.Harness
	from string // the version installed now; "" when it is not installed
	to   string // the latest version, when known
}

// install installs the harnesses that are missing, at their latest version.
// With --update it looks for newer versions of the installed ones instead.
// Either way the user ticks what to act on before anything is run.
func (e *env) install(ctx context.Context, args []string) error {
	update := false
	var names []string
	for _, a := range args {
		switch {
		case a == updateFlag:
			update = true
		case strings.HasPrefix(a, "-"):
			return errors.New("usage: install [" + updateFlag + "] [harness ...]")
		default:
			names = append(names, a)
		}
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = cfg.Names()
	}
	for _, name := range names {
		if _, _, err := cfg.Lookup(name); err != nil {
			return err
		}
	}

	var jobs []installJob
	for _, name := range names {
		h := cfg.Harnesses[name]
		job, note := e.installJob(ctx, name, h, update)
		if job == nil {
			fmt.Fprintf(e.out, "  %-10s %s\n", name, note)
			continue
		}
		jobs = append(jobs, *job)
	}
	if len(jobs) == 0 {
		if update {
			fmt.Fprintln(e.out, "\nNothing to update.")
		} else {
			fmt.Fprintln(e.out, "\nNothing to install. Look for newer versions with: "+e.st.accent.Render("install "+updateFlag))
		}
		return nil
	}

	question, verb := "Install these harnesses?", "install"
	if update {
		question, verb = "Would you like to update?", "update"
	}
	options := make([]console.Option, len(jobs))
	for n, j := range jobs {
		options[n] = console.Option{ID: j.name, Label: j.label(), Checked: true}
	}
	chosen, ok := e.ui.Choose(question, options)
	if !ok || len(chosen) == 0 {
		fmt.Fprintln(e.out, "Nothing was changed.")
		return nil
	}

	failed := 0
	for _, j := range jobs {
		if !slices.Contains(chosen, j.name) {
			continue
		}
		if ctx.Err() != nil {
			return errors.New(verb + " was interrupted")
		}
		if err := e.runInstall(ctx, j); err != nil {
			failed++
			fmt.Fprintf(e.out, "%s %s: %v\n", e.st.bad.Render("✗"), j.name, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("could not %s %s", verb, harnessCount(failed))
	}
	fmt.Fprintln(e.out, e.st.dim.Render("Running sessions keep the version they started with. Check the result with: ")+e.st.accent.Render("doctor"))
	return nil
}

// installJob decides whether a harness is offered to the user. When it is
// not, the note says why.
func (e *env) installJob(ctx context.Context, name string, h config.Harness, update bool) (job *installJob, note string) {
	st := e.st
	_, err := exec.LookPath(h.Command)
	installed := err == nil
	current := ""
	if installed && len(h.VersionArgs) > 0 {
		vctx, cancel := context.WithTimeout(ctx, versionTimeout)
		current, _ = harness.Version(vctx, h, e.environ)
		cancel()
	}

	switch {
	case !update && installed:
		return nil, st.good.Render("✓") + " already installed " + st.dim.Render(current)
	case update && !installed:
		return nil, st.dim.Render("- not installed; install it with: ") + st.accent.Render("install "+name)
	case len(h.InstallCommand) == 0:
		return nil, st.warn.Render("!") + " has no install_command in the config, so H2H cannot install it"
	}
	if _, err := exec.LookPath(h.InstallCommand[0]); err != nil {
		return nil, st.warn.Render("!") + fmt.Sprintf(" is installed with %s, which is not installed or not on PATH", h.InstallCommand[0])
	}
	if !update {
		return &installJob{name: name, h: h}, ""
	}

	if current == "" {
		return nil, st.warn.Render("!") + " cannot tell which version is installed; set version_args in the config"
	}
	if len(h.LatestCommand) == 0 {
		return nil, st.warn.Render("!") + " has no latest_command in the config, so H2H cannot tell whether " + current + " is the latest"
	}
	lctx, cancel := context.WithTimeout(ctx, latestTimeout)
	latest, err := harness.Latest(lctx, h, e.environ)
	cancel()
	switch {
	case err != nil:
		return nil, st.warn.Render("!") + " could not find the latest version: " + err.Error()
	case harness.Number(latest) == "" || harness.Number(current) == "":
		return nil, st.warn.Render("!") + fmt.Sprintf(" cannot compare the installed %q with the latest %q", current, latest)
	case !harness.Newer(latest, current):
		return nil, st.good.Render("✓") + " up to date " + st.dim.Render(harness.Number(current))
	}
	return &installJob{name: name, h: h, from: harness.Number(current), to: harness.Number(latest)}, ""
}

// label is how a job reads in the dialog: what changes, and the command
// that makes the change.
func (j installJob) label() string {
	change := "not installed"
	if j.from != "" {
		change = j.from + " → " + j.to
	}
	return fmt.Sprintf("%-10s %-22s %s", j.name, change, strings.Join(j.h.InstallCommand, " "))
}

// runInstall installs or updates one harness and checks that it took.
func (e *env) runInstall(ctx context.Context, j installJob) error {
	fmt.Fprintf(e.out, "\n%s %s\n", e.st.title.Render(j.name), e.st.dim.Render(strings.Join(j.h.InstallCommand, " ")))
	ictx, cancel := context.WithTimeout(ctx, installTimeout)
	err := harness.Install(ictx, j.h, e.environ, e.out, e.errw)
	cancel()
	if err != nil {
		return fmt.Errorf("%s failed: %w", j.h.InstallCommand[0], err)
	}

	path, err := exec.LookPath(j.h.Command)
	if err != nil {
		return fmt.Errorf("the install ran, but %q is still not on PATH; add the directory it was installed in to PATH", j.h.Command)
	}
	now := ""
	if len(j.h.VersionArgs) > 0 {
		vctx, cancel := context.WithTimeout(ctx, versionTimeout)
		now, err = harness.Version(vctx, j.h, e.environ)
		cancel()
		if err != nil {
			return fmt.Errorf("it was installed but does not run: %w", err)
		}
	}
	// An update that leaves the version unchanged means another copy of
	// the harness comes first on PATH.
	if j.from != "" && !harness.Newer(now, j.from) {
		return fmt.Errorf("the update ran, but %s is still %s; another copy of it comes first on PATH", path, harness.Number(now))
	}
	fmt.Fprintf(e.out, "%s %s %s\n", e.st.good.Render("✓"), j.name, e.st.dim.Render(strings.TrimSpace("now "+now)))
	return nil
}

// harnessCount is a count of harnesses in words.
func harnessCount(n int) string {
	if n == 1 {
		return "1 harness"
	}
	return fmt.Sprintf("%d harnesses", n)
}
