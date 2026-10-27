// Package execx wraps os/exec with context, extra environment, captured
// output and a portable shell.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Cmd describes a command invocation.
type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string // extra KEY=VALUE pairs appended to os.Environ()
	Stdin  io.Reader
	Stdout io.Writer // optional tee for stdout
	Stderr io.Writer // optional tee for stderr
}

// Result holds the outcome of a finished command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Combined returns stdout followed by stderr, trimmed.
func (r Result) Combined() string {
	return strings.TrimSpace(string(r.Stdout) + "\n" + string(r.Stderr))
}

// ExitError reports a non-zero exit.
type ExitError struct {
	Cmd    string
	Code   int
	Output string
}

func (e *ExitError) Error() string {
	out := e.Output
	if len(out) > 2000 {
		out = "..." + out[len(out)-2000:]
	}
	if out == "" {
		return fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code)
	}
	return fmt.Sprintf("%s: exit status %d: %s", e.Cmd, e.Code, out)
}

// Run executes the command. A non-zero exit is returned as *ExitError
// together with the populated Result.
func Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = c.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = tee(&stdout, c.Stdout)
	cmd.Stderr = tee(&stderr, c.Stderr)
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%s: %w", describe(c), ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &ExitError{Cmd: describe(c), Code: res.ExitCode, Output: res.Combined()}
	}
	return res, fmt.Errorf("%s: %w", describe(c), err)
}

// Output runs the command and returns trimmed stdout.
func Output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	res, err := Run(ctx, Cmd{Name: name, Args: args, Dir: dir})
	return strings.TrimSpace(string(res.Stdout)), err
}

// Shell runs a script through the given shell (for example ["sh","-c"]).
func Shell(ctx context.Context, shell []string, c Cmd, script string) (Result, error) {
	if len(shell) == 0 {
		shell = DefaultShell()
	}
	c.Name = shell[0]
	c.Args = append(append([]string{}, shell[1:]...), script)
	return Run(ctx, c)
}

// DefaultShell returns a POSIX shell when one is available. On Windows it
// prefers the sh.exe shipped with Git for Windows and falls back to cmd.
func DefaultShell() []string {
	if p, err := exec.LookPath("sh"); err == nil {
		return []string{p, "-c"}
	}
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), `C:\Program Files`} {
			if root == "" {
				continue
			}
			p := filepath.Join(root, "Git", "bin", "sh.exe")
			if _, err := os.Stat(p); err == nil {
				return []string{p, "-c"}
			}
		}
		return []string{"cmd", "/C"}
	}
	return []string{"/bin/sh", "-c"}
}

func tee(buf *bytes.Buffer, w io.Writer) io.Writer {
	if w == nil {
		return buf
	}
	return io.MultiWriter(buf, w)
}

func describe(c Cmd) string {
	s := c.Name
	if base := filepath.Base(s); base != "" {
		s = base
	}
	if len(c.Args) > 0 {
		s += " " + strings.Join(c.Args, " ")
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}
