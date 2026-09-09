//go:build darwin || linux

package shell

import (
	"bytes"
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
// With the probe off no request is sent at all, so nothing can be lost.
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
	if n := strings.Count(transcript.String(), dsrQuery); n != 0 {
		t.Errorf("the editor sent %d cursor-position requests, want 0", n)
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
