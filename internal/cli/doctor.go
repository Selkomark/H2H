package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

const (
	// versionTimeout bounds a harness printing its version.
	versionTimeout = 15 * time.Second
	// loginTimeout bounds the one short answer that proves a login.
	loginTimeout = 2 * time.Minute
	// loginPrompt is what a harness is asked to prove it can reach its model.
	loginPrompt = "Reply with the single word OK and nothing else. Do not read or change any file."
)

// diagnosis collects the results of doctor's checks and prints each one as
// it is known, so a slow check does not hold back the ones before it.
type diagnosis struct {
	e      *env
	failed int
	warned int
}

func (d *diagnosis) section(title string) {
	fmt.Fprintln(d.e.out, "\n"+d.e.st.title.Render(title))
}

func (d *diagnosis) line(mark, name, detail string) {
	fmt.Fprintf(d.e.out, "  %s %-18s %s\n", mark, name, detail)
}

func (d *diagnosis) ok(name, detail string) {
	d.line(d.e.st.good.Render("✓"), name, detail)
}

// warn reports something that limits H2H without stopping it.
func (d *diagnosis) warn(name, detail, fix string) {
	d.warned++
	d.line(d.e.st.warn.Render("!"), name, detail)
	d.fix(fix)
}

// fail reports something that will make a command fail.
func (d *diagnosis) fail(name, detail, fix string) {
	d.failed++
	d.line(d.e.st.bad.Render("✗"), name, detail)
	d.fix(fix)
}

func (d *diagnosis) fix(fix string) {
	if fix != "" {
		fmt.Fprintf(d.e.out, "    %-18s %s\n", "", d.e.st.dim.Render("Fix: ")+fix)
	}
}

// doctor checks that everything H2H needs is in place: the system, the
// project, the config, and every configured harness, including its login
// and whether it trusts the project. It changes nothing. Proving a login
// costs one short answer from each harness.
func (e *env) doctor(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: doctor")
	}
	d := &diagnosis{e: e}
	d.system(ctx)
	if cfg, ok := d.config(); ok {
		d.harnesses(ctx, cfg)
		if e.root != "" {
			d.permission(cfg)
		}
	}

	fmt.Fprintln(e.out)
	if d.failed > 0 {
		return fmt.Errorf("doctor found %s; fix the lines marked ✗ and run it again", plural(d.failed, "problem"))
	}
	verdict := e.st.good.Render("Everything H2H needs is in place.")
	if d.warned > 0 {
		verdict = e.st.good.Render("H2H can run.") + " " + plural(d.warned, "warning") + " above."
	}
	fmt.Fprintln(e.out, verdict)
	return nil
}

// system checks git, the project and what a keeper needs from the machine.
func (d *diagnosis) system(ctx context.Context) {
	e := d.e
	d.section("System")

	version, err := workspace.GitVersion(ctx)
	if err != nil {
		d.fail("git", err.Error(), "install git 2.17 or later; every session works in a git worktree")
	} else {
		d.ok("git", version)
	}

	switch {
	case e.root == "":
		d.fail("project", "this directory is not inside a git repository",
			"run h2h inside the project, or create one with: git init")
	default:
		d.ok("project", displayPath(e.root, os.Getenv("HOME"), 60))
		if err := workspace.Ready(ctx, e.root); err != nil {
			d.fail("first commit", err.Error(), "a session's workspace is a copy of a commit; make one with: git commit")
		} else {
			d.ok("first commit", "workspaces can be created")
		}
		if err := session.Writable(e.root); err != nil {
			d.fail("state directory", err.Error(), "H2H keeps sessions and workspaces in "+session.StateDir(e.root)+"; make it writable")
		} else {
			d.ok("state directory", "writable")
		}
	}

	if err := keeper.Check(e.root); err != nil {
		d.fail("sessions", err.Error(), "a session needs a pseudo-terminal and a local socket under /tmp")
	} else {
		d.ok("sessions", "pseudo-terminal and local socket work")
	}

	if argv, err := editorCommand(os.Getenv, exec.LookPath); err != nil {
		d.warn("editor", err.Error(), "only \"config "+manualFlag+"\" needs it")
	} else if _, err := exec.LookPath(argv[0]); err != nil {
		d.warn("editor", argv[0]+" is not installed or not on PATH", "set EDITOR to an installed editor; only \"config "+manualFlag+"\" needs it")
	} else {
		d.ok("editor", argv[0])
	}
}

// config checks the project's config and returns it. Outside a project the
// shipped defaults stand in, so the harnesses can still be checked.
func (d *diagnosis) config() (config.Config, bool) {
	e := d.e
	d.section("Config")
	if e.root == "" {
		cfg, err := config.Parse(config.Template())
		if err != nil {
			d.fail(config.FileName, err.Error(), "")
			return cfg, false
		}
		d.ok(config.FileName, "no project; checking the shipped defaults")
		return cfg, true
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		// The error says what is wrong: a line that does not parse, a
		// setting H2H does not know, or a value it does not accept.
		d.fail(config.FileName, strings.TrimPrefix(err.Error(), config.FileName+": "),
			"correct it with: "+e.st.accent.Render("/config "+manualFlag)+", or have it rewritten with: "+e.st.accent.Render("/config"))
		return cfg, false
	}
	if _, exists, _ := e.configText(); !exists {
		d.warn(config.FileName, "none yet, so the shipped defaults are in force; set it up with "+e.st.accent.Render("/config"), "")
		return cfg, true
	}
	d.ok(config.FileName, fmt.Sprintf("valid (harnesses: %d, domains: %d)", len(cfg.Harnesses), len(cfg.Domains)))
	// A config written by an earlier H2H lacks the settings added since,
	// and goes without what they switch on. The lines to add are shown.
	if shipped, err := config.Parse(config.Template()); err == nil {
		for _, name := range cfg.Names() {
			if lacks := cfg.Harnesses[name].Lacks(shipped.Harnesses[name]); len(lacks) > 0 {
				d.warn(name, fmt.Sprintf("lacks %s that H2H ships for it; add these lines to [harness.%s] with %s:", plural(len(lacks), "setting"), name, e.st.accent.Render("/config "+manualFlag)), "")
				for _, line := range lacks {
					fmt.Fprintf(e.out, "    %-18s %s\n", "", e.st.dim.Render(line))
				}
			}
		}
	}
	return cfg, true
}

// harnesses checks every configured harness the same way: installed, runs,
// and logged in, which it proves by asking for one short answer. A harness
// nothing depends on only warns.
func (d *diagnosis) harnesses(ctx context.Context, cfg config.Config) {
	e := d.e
	d.section("Harnesses")
	for _, name := range cfg.Names() {
		h := cfg.Harnesses[name]
		report := d.warn
		why := "nothing depends on it, so only \"start " + name + "\" needs it"
		if owned := ownedDomains(cfg, name); name == cfg.DefaultHarness || len(owned) > 0 {
			report = d.fail
			why = "it owns the domain " + strings.Join(owned, ", ")
			if name == cfg.DefaultHarness {
				why = "it is the default harness"
			}
		}
		label := name
		if name == cfg.DefaultHarness {
			label += " (default)"
		}

		path, err := exec.LookPath(h.Command)
		if err != nil {
			fix := "install it, or change this harness in the config; " + why
			if len(h.InstallCommand) > 0 {
				fix = "install it with: " + e.st.accent.Render("install "+name) + "; " + why
			}
			report(label, fmt.Sprintf("the program %q is not installed or not on PATH", h.Command), fix)
			continue
		}
		detail := displayPath(path, os.Getenv("HOME"), 50)
		if len(h.VersionArgs) > 0 {
			vctx, cancel := context.WithTimeout(ctx, versionTimeout)
			version, err := harness.Version(vctx, h, e.environ)
			cancel()
			if err != nil {
				report(label, fmt.Sprintf("%s is installed but does not run: %v", h.Command, err), "reinstall it; "+why)
				continue
			}
			detail = version
		}
		if len(h.AskArgs) == 0 {
			d.warn(label, detail+"; login not checked, the harness has no ask_args", "")
			continue
		}
		actx, cancel := context.WithTimeout(ctx, loginTimeout)
		var out, errOut bytes.Buffer
		err = harness.Ask(actx, h, e.environ, e.askDir(), loginPrompt, &out, &errOut)
		timedOut := actx.Err() != nil
		cancel()
		if err != nil {
			reason := firstLine(errOut.String() + "\n" + out.String())
			if timedOut {
				reason = "no answer within " + loginTimeout.String()
			}
			report(label, fmt.Sprintf("%s; it could not answer: %s", detail, reason),
				"start "+h.Command+" yourself and log in; "+why)
			continue
		}
		d.ok(label, detail+", logged in and answering")
	}
}

// permission reads, for every installed harness, whether the user has
// told it to trust the project. A harness that has not will stop at its
// trust question when a session starts, until someone attaches and answers.
func (d *diagnosis) permission(cfg config.Config) {
	e := d.e
	d.section("Permission")
	home, err := os.UserHomeDir()
	if err != nil {
		d.fail("home directory", err.Error(), "")
		return
	}
	for _, name := range cfg.Names() {
		h := cfg.Harnesses[name]
		if _, err := exec.LookPath(h.Command); err != nil {
			continue // reported under Harnesses
		}
		if !h.ChecksTrust() {
			d.warn(name, "not checked: the harness has no trust_file in the config", "")
			continue
		}
		record := displayPath(harness.TrustPath(h, home), home, 50)
		trusted, err := harness.Trusted(h, e.root, home)
		switch {
		case err != nil:
			d.warn(name, "could not read its trust record: "+err.Error(), "")
		case trusted:
			d.ok(name, "trusts this project "+e.st.dim.Render("("+record+")"))
		default:
			d.warn(name, "has not been told to trust this project "+e.st.dim.Render("("+record+")"),
				"run "+e.st.accent.Render(h.Command)+" once in the project and accept its trust question; until then a session waits at that question")
		}
	}
}

// askDir is where a harness is run for a single answer: the project, or
// the current directory when there is none.
func (e *env) askDir() string {
	if e.root != "" {
		return e.root
	}
	return "."
}

// ownedDomains returns the domains a harness owns, in order.
func ownedDomains(cfg config.Config, harnessName string) []string {
	var out []string
	for _, n := range cfg.DomainNames() {
		if cfg.Domains[n].Harness == harnessName {
			out = append(out, n)
		}
	}
	return slices.Clip(out)
}

// firstLine returns the first line of text that is not blank.
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "it gave no reason"
}
