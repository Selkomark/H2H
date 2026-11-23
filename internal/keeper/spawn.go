package keeper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Selkomark/H2H/internal/session"
)

// Command is the internal argument that starts the h2h binary as a keeper.
const Command = "keeper"

const spawnTimeout = 10 * time.Second

// Spawn starts a detached keeper for an existing session record and waits
// until its harness is running.
func Spawn(root, id string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	klog, err := os.OpenFile(filepath.Join(session.Dir(root, id), "keeper.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer klog.Close()

	cmd := exec.Command(exe, Command, root, id)
	cmd.Stdout, cmd.Stderr = klog, klog
	// Its own session: closing the launching terminal does not reach it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if it ends while we are alive

	deadline := time.Now().Add(spawnTimeout)
	for time.Now().Before(deadline) {
		info, err := session.Load(root, id)
		if err != nil {
			return err
		}
		switch info.State {
		case session.Running:
			return nil
		case session.Exited:
			if info.Error != "" {
				return errors.New(info.Error)
			}
			return nil // a harness that ended at once still started
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("session %s did not start within %s", id, spawnTimeout)
}

// Stop asks the keeper of a running session to end its harness, and waits
// for the session to be recorded as exited.
func Stop(root string, info session.Info) error {
	if info.State != session.Running {
		return fmt.Errorf("session %s is %s, not running", info.ID, info.State)
	}
	if err := syscall.Kill(info.KeeperPID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling keeper: %w", err)
	}
	deadline := time.Now().Add(stopGrace + 5*time.Second)
	for time.Now().Before(deadline) {
		now, err := session.Load(root, info.ID)
		if err != nil {
			return err
		}
		if !now.Live() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("session %s did not stop in time", info.ID)
}
