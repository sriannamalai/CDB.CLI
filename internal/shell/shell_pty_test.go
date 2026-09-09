//go:build darwin || linux

package shell

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// ptyChildEnv names the file the child process writes the accepted line to. It
// also marks the process as the child, so the helper test below stays inert in
// an ordinary run.
const ptyChildEnv = "CDB_SHELL_PTY_LINE_FILE"

// dsrQuery is the cursor-position request readline sends when the
// cursor-position-probe option is on, and dsrReply the terminal's answer.
const (
	dsrQuery = "\x1b[6n"
	dsrReply = "\x1b[1;13R"
)

// keyboard is the far end of the terminal: it types and it answers the
// cursor-position request. An answer that has come due while a keystroke is
// pending rides along in the same write, which is what a real terminal does
// when the operator keeps typing during the round trip, and is the case the
// library gets wrong.
type keyboard struct {
	mu      sync.Mutex
	w       *os.File
	pending int
	typing  bool
}

func (k *keyboard) write(s string) error {
	_, err := k.w.WriteString(s)
	return err
}

// answer delivers one cursor-position answer, holding it back for the next
// keystroke while the operator is typing.
func (k *keyboard) answer() error {
	k.mu.Lock()
	if k.typing {
		k.pending++
		k.mu.Unlock()
		return nil
	}
	k.mu.Unlock()
	return k.write(dsrReply)
}

// press types one character, carrying any answer that has come due with it.
func (k *keyboard) press(r rune) error {
	k.mu.Lock()
	held := k.pending
	k.pending = 0
	k.mu.Unlock()
	return k.write(strings.Repeat(dsrReply, held) + string(r))
}

func (k *keyboard) startTyping() {
	k.mu.Lock()
	k.typing = true
	k.mu.Unlock()
}

// stopTyping releases any answer still held back, so the editor is never left
// waiting for one.
func (k *keyboard) stopTyping() error {
	k.mu.Lock()
	held := k.pending
	k.pending = 0
	k.typing = false
	k.mu.Unlock()
	if held == 0 {
		return nil
	}
	return k.write(strings.Repeat(dsrReply, held))
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

// setWinsize gives the terminal a size; readline lays the line out against it.
func setWinsize(f *os.File, rows, cols uint16) error {
	size := struct{ rows, cols, x, y uint16 }{rows, cols, 0, 0}
	return ioctl(f.Fd(), tiocSWinsz, uintptr(unsafe.Pointer(&size)))
}

// ptyTranscript collects everything the shell writes to the terminal and lets
// the test wait for a fragment of it.
type ptyTranscript struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *ptyTranscript) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(p)
}

func (t *ptyTranscript) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func (t *ptyTranscript) waitFor(s string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(t.String(), s) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestReadlineKeepsEveryKeystrokeOnALaggyTerminal drives the real line editor
// over a pseudo-terminal that answers a cursor-position request only after a
// delay, the way a terminal at the far end of an SSH link does. With the probe
// on, the answer lands in the same read as the next keystroke and the library
// throws both away, so the line arrives with characters missing (issue #33).
// With the library bump in place the answer is parsed out of that read and the
// keystroke beside it is kept (upstream PR #118), so the line arrives whole.
func TestReadlineKeepsEveryKeystrokeOnALaggyTerminal(t *testing.T) {
	if os.Getenv(ptyChildEnv) != "" {
		t.Skip("running as the pty child")
	}
	const (
		typed     = "find rr-db selector type order limit ten now"
		keyDelay  = 60 * time.Millisecond
		dsrDelay  = 50 * time.Millisecond
		startWait = 10 * time.Second
	)

	master, slaveName, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer func() { _ = master.Close() }()
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", slaveName, err)
	}
	if err := setWinsize(slave, 30, 120); err != nil {
		t.Fatalf("set window size: %v", err)
	}

	lineFile := filepath.Join(t.TempDir(), "line")
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellPtyChild$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), ptyChildEnv+"="+lineFile)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the line editor: %v", err)
	}
	_ = slave.Close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Read the terminal, answering every cursor-position request after the
	// round trip a slow link would impose.
	kbd := &keyboard{w: master}
	var transcript ptyTranscript
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				_, _ = transcript.Write(chunk)
				for range strings.Count(string(chunk), dsrQuery) {
					go func() {
						time.Sleep(dsrDelay)
						_ = kbd.answer()
					}()
				}
			}
			if err != nil {
				return
			}
		}
	}()

	if !transcript.waitFor("cdb> ", startWait) {
		t.Fatalf("the prompt never appeared; terminal said %q", transcript.String())
	}
	kbd.startTyping()
	for _, r := range typed {
		time.Sleep(keyDelay)
		if err := kbd.press(r); err != nil {
			t.Fatalf("type %q: %v", r, err)
		}
	}
	if err := kbd.stopTyping(); err != nil {
		t.Fatalf("answer the pending cursor-position requests: %v", err)
	}
	time.Sleep(dsrDelay)
	if err := kbd.write("\r"); err != nil {
		t.Fatalf("accept the line: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the line editor exited with %v; terminal said %q", err, transcript.String())
		}
	case <-time.After(startWait):
		t.Fatalf("the line was never accepted; terminal said %q", transcript.String())
	}

	got, err := os.ReadFile(lineFile)
	if err != nil {
		t.Fatalf("read the accepted line: %v", err)
	}
	if string(got) != typed {
		t.Errorf("accepted line = %q, want %q", got, typed)
	}
	// The probe is on, so the terminal was asked where the cursor is and the
	// answers arrived 50 ms late, riding along with the next keystroke. PR #118
	// is what keeps the keystroke when the answer is parsed out of that read;
	// the accepted line above is the proof. Assert the probe really happened,
	// so this test cannot pass by the request never being sent.
	if n := strings.Count(transcript.String(), dsrQuery); n == 0 {
		t.Errorf("the editor sent no cursor-position requests, so the dropped-keystroke path was never exercised")
	}
}

// TestShellPtyChild is the other half of the test above: it runs the real line
// editor on the pseudo-terminal it inherits and writes the accepted line where
// the parent can read it. It does nothing in an ordinary run.
func TestShellPtyChild(t *testing.T) {
	lineFile := os.Getenv(ptyChildEnv)
	if lineFile == "" {
		t.Skip("only runs as the child of TestReadlineKeepsEveryKeystrokeOnALaggyTerminal")
	}
	sess := session.New(os.Stdin, os.Stdout, os.Stderr)
	sh, err := New(command.Default(), sess, Config{Keymap: "emacs"})
	if err != nil {
		t.Fatalf("new shell: %v", err)
	}
	if err := sh.initReadline(); err != nil {
		t.Fatalf("init readline: %v", err)
	}
	line, err := sh.rl.Readline()
	if err != nil {
		t.Fatalf("readline: %v", err)
	}
	if err := os.WriteFile(lineFile, []byte(line), 0o600); err != nil {
		t.Fatalf("write the accepted line: %v", err)
	}
}

// ptyBottomChildEnv names the file the bottom-row child touches when it is
// done. It also marks the process as that child, so the helper stays inert in
// an ordinary run.
const ptyBottomChildEnv = "CDB_SHELL_PTY_BOTTOM_FILE"

// dsrBottomReply answers the cursor-position request with the row the child's
// cursor is really on: it printed exactly ptyBottomRows lines on a window that
// tall, so the cursor sits on the last row at column 1. The shared dsrReply
// says row 1, which would leave the library believing there are nine rows below
// the prompt and would hide the very bug this test is about.
const (
	ptyBottomRows  = 10
	dsrBottomReply = "\x1b[10;1R"
)

// TestPromptSurvivesOutputThatFillsTheWindow drives the real line editor on a
// short (80x10) terminal after output has reached the last row, which is the
// case that broke on macOS in b1fd070: with the cursor-position probe off the
// library leaves startRows at -1, ensureInputSpace returns at its
// "if e.startRows < 1" guard without reserving a row below the prompt, and the
// clear-below in the same redraw then erases the prompt it has just printed.
// The assertions are on the escapes, not on a screenshot: the probe must be
// sent, and the reservation it enables — a newline followed by a one-row climb
// back — must appear.
func TestPromptSurvivesOutputThatFillsTheWindow(t *testing.T) {
	if os.Getenv(ptyChildEnv) != "" || os.Getenv(ptyBottomChildEnv) != "" {
		t.Skip("running as a pty child")
	}
	const (
		rows      = ptyBottomRows
		cols      = 80
		startWait = 10 * time.Second
	)

	master, slaveName, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer func() { _ = master.Close() }()
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", slaveName, err)
	}
	if err := setWinsize(slave, rows, cols); err != nil {
		t.Fatalf("set window size: %v", err)
	}

	doneFile := filepath.Join(t.TempDir(), "done")
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellPtyBottomRowChild$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), ptyBottomChildEnv+"="+doneFile)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the line editor: %v", err)
	}
	_ = slave.Close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Answer every cursor-position request at once, with the same constant
	// dsrBottomReply each time. That reply is truthful, not a shortcut: the
	// child prints exactly ptyBottomRows filler lines before it ever asks, so
	// its cursor really is on that row for every request in this test — this
	// test is about where the prompt lands, not about latency.
	var transcript ptyTranscript
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				_, _ = transcript.Write(chunk)
				for range strings.Count(string(chunk), dsrQuery) {
					_, _ = master.WriteString(dsrBottomReply)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// The child prints "filler-1".."filler-10" and then draws the prompt.
	if !transcript.waitFor("filler-10", startWait) {
		t.Fatalf("the filler never appeared; terminal said %q", transcript.String())
	}
	if !transcript.waitFor("cdb> ", startWait) {
		t.Fatalf("the prompt never appeared after the window filled; terminal said %q", transcript.String())
	}
	if _, err := master.WriteString("\r"); err != nil {
		t.Fatalf("accept the line: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the line editor exited with %v; terminal said %q", err, transcript.String())
		}
	case <-time.After(startWait):
		t.Fatalf("the line was never accepted; terminal said %q", transcript.String())
	}
	if _, err := os.Stat(doneFile); err != nil {
		t.Fatalf("the child never finished: %v", err)
	}

	// Two escapes, both of which exist only when the library knows which row
	// the prompt starts on. Without the probe it cannot know, ensureInputSpace
	// bails at its startRows < 1 guard, and the clear-below in the same redraw
	// wipes the prompt.
	after := transcript.String()
	if i := strings.Index(after, "filler-10"); i >= 0 {
		after = after[i:]
	}
	if !strings.Contains(after, dsrQuery) {
		t.Errorf("the editor never asked where the cursor was, so it cannot know it is on the bottom row; terminal said %q", transcript.String())
	}
	// ensureInputSpace scrolls the window up by the missing row (one CRLF) and
	// climbs back one row to the new prompt start. That pair is the reservation
	// that keeps the prompt off the last row.
	if !strings.Contains(after, "\r\n\x1b[1A") {
		t.Errorf("the editor did not reserve a row below the prompt on the bottom row; terminal said %q", transcript.String())
	}
}

// TestShellPtyBottomRowChild is the other half of the test above. It fills the
// window with exactly as many lines as the terminal has rows, so the cursor is
// on the last row when the editor first draws its prompt, then reads one line.
// It does nothing in an ordinary run.
func TestShellPtyBottomRowChild(t *testing.T) {
	doneFile := os.Getenv(ptyBottomChildEnv)
	if doneFile == "" {
		t.Skip("only runs as the child of TestPromptSurvivesOutputThatFillsTheWindow")
	}
	for i := 1; i <= ptyBottomRows; i++ {
		fmt.Fprintf(os.Stdout, "filler-%d\n", i)
	}
	sess := session.New(os.Stdin, os.Stdout, os.Stderr)
	sh, err := New(command.Default(), sess, Config{Keymap: "emacs"})
	if err != nil {
		t.Fatalf("new shell: %v", err)
	}
	if err := sh.initReadline(); err != nil {
		t.Fatalf("init readline: %v", err)
	}
	if _, err := sh.rl.Readline(); err != nil {
		t.Fatalf("readline: %v", err)
	}
	if err := os.WriteFile(doneFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write the done marker: %v", err)
	}
}
