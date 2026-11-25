package console

import (
	"errors"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// isClaim reports whether a command is the one that asks for the mouse.
// Commands are compared, not run, because a timer is a command too and
// running it would wait it out.
func isClaim(cmd tea.Cmd) bool {
	return cmd != nil && reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(claimMouse).Pointer()
}

func TestClaimMouseAsksForAllMotion(t *testing.T) {
	if got, want := claimMouse(), tea.EnableMouseAllMotion(); got != want {
		t.Errorf("claimMouse() = %#v, want Bubble Tea's own %#v", got, want)
	}
}

func TestMouseIsClaimedAgainWheneverItMayHaveBeenLost(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{"the window gets the focus back", tea.FocusMsg{}},
		{"the window changes size", tea.WindowSizeMsg{Width: 80, Height: 24}},
		{"an attached harness or an editor returns the terminal", foregroundDoneMsg{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(func(string) bool { return false })
			if _, cmd := m.Update(tt.msg); !isClaim(cmd) {
				t.Errorf("after %T the console did not ask for the mouse", tt.msg)
			}
		})
	}
}

func TestTimerClaimsTheMouseAndKeepsRunning(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	_, cmd := m.Update(mouseTickMsg{})
	if cmd == nil {
		t.Fatal("the timer's turn did nothing")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("the timer's turn gave %T; want the claim and the next turn", cmd())
	}
	if !isClaim(batch[0]) {
		t.Error("the timer's turn did not ask for the mouse")
	}
	if batch[1] == nil || isClaim(batch[1]) {
		t.Error("the timer's turn did not set the next one")
	}
}

func TestHandingTheTerminalBackAnswersAndClaimsTheMouse(t *testing.T) {
	reply := make(chan error, 1)
	failed := errors.New("editor failed")
	msg := foregroundDone(reply)(failed)
	if got := <-reply; got != failed {
		t.Errorf("the command that asked for the terminal got %v, want %v", got, failed)
	}
	m := newTestModel(func(string) bool { return false })
	if _, cmd := m.Update(msg); !isClaim(cmd) {
		t.Errorf("what the hand-back sends the console (%T) did not make it ask for the mouse", msg)
	}
}

func TestLostReleaseDoesNotLeaveTheSelectionFollowingThePointer(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.print("some text on the page to select\n")
	x, y := find(t, m, "some text")

	// A drag starts, and its release never arrives.
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x + 4, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if m.sel == nil || !m.sel.dragging {
		t.Fatal("a press and a move did not start a drag")
	}
	head := m.sel.head

	// The pointer then moves with no button held.
	m.Update(tea.MouseMsg{X: x + 9, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if m.sel.dragging {
		t.Error("the drag went on after the pointer moved with no button held")
	}
	if m.sel.head != head {
		t.Errorf("the selection followed the pointer to %v, want it left at %v", m.sel.head, head)
	}

	// A click afterwards works as usual.
	tx, ty := find(t, m, "Sessions")
	click(m, tx+2, ty)
	if m.tab != 1 {
		t.Errorf("a click after the lost release: tab = %d, want 1", m.tab)
	}
}

func TestLosingFocusEndsADrag(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.print("some text on the page to select\n")
	x, y := find(t, m, "some text")
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x + 4, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m.Update(tea.BlurMsg{})
	if m.sel == nil || m.sel.dragging {
		t.Errorf("after losing focus: selection = %+v; want it kept, and no longer dragging", m.sel)
	}
	if m.hoverRow != -1 {
		t.Errorf("after losing focus: hoverRow = %d, want none", m.hoverRow)
	}
}
