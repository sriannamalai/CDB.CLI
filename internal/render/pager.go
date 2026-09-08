package render

import (
	"io"
	"os"
	"os/exec"
	"strings"
)

// pagerCommand resolves the pager command line. It returns "" when paging is
// disabled by the setting or the environment.
func pagerCommand(setting string) string {
	switch setting {
	case "", "off", "none":
		return ""
	case "auto":
		if p := os.Getenv("PAGER"); p != "" {
			return p
		}
		return "less -R"
	default:
		return setting
	}
}

// pager runs a pager process and exposes its stdin.
type pager struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

// startPager launches the pager, writing its output to out. It returns nil when
// paging is disabled or the pager binary is missing.
func startPager(command string, out io.Writer) *pager {
	if command == "" {
		return nil
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	if _, err := exec.LookPath(fields[0]); err != nil {
		return nil
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	return &pager{cmd: cmd, in: in}
}

func (p *pager) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *pager) Close() error {
	if err := p.in.Close(); err != nil {
		return err
	}
	return p.cmd.Wait()
}
