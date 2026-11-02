//go:build darwin || linux

package task_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/go-task/task/v3"
)

const statusTaskfile = `version: '3'
tasks:
  ok:
    cmds: ['echo payload']
  fail:
    cmds: ['exit 7']
  ignored:
    ignore_error: true
    cmds: ['exit 7']
  cleanup:
    cmds:
      - defer: echo cleanup-finished >&2
      - echo payload
  slow:
    cmds: ['sleep 0.1; echo slow-finished >&2']
  confirm:
    prompt: 'Do not expose secret-confirmation-value'
    cmds: ['echo confirmed']
`

// Exercise the real terminal gate and executor hooks, not an injected reporter.
func TestTerminalStatusExecution(t *testing.T) { //nolint:paralleltest // isolates inherited terminal ownership
	t.Setenv("_TASK_TERMINAL_PARENT", "")
	for _, tc := range []struct {
		name                      string
		calls                     []string
		parallel, fail, assumeYes bool
		input, terminalState      string
	}{
		{name: "success", calls: []string{"ok"}, terminalState: "done"},
		{name: "failure", calls: []string{"fail"}, fail: true, terminalState: "error"},
		{name: "ignored failure", calls: []string{"ignored"}, terminalState: "done"},
		{name: "deferred cleanup", calls: []string{"cleanup"}, terminalState: "done"},
		{name: "parallel success", calls: []string{"ok", "slow"}, parallel: true, terminalState: "done"},
		{name: "parallel failure", calls: []string{"fail", "slow"}, parallel: true, fail: true, terminalState: "error"},
		{name: "approval", calls: []string{"confirm"}, input: "y\n", terminalState: "done"},
		{name: "declined", calls: []string{"confirm"}, input: "n\n", fail: true, terminalState: "idle"},
		{name: "assume yes", calls: []string{"confirm"}, assumeYes: true, terminalState: "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(statusTaskfile), 0o600))
			master, slave, err := pty.Open()
			require.NoError(t, err)
			t.Cleanup(func() { _ = slave.Close(); _ = master.Close() })
			captured := make(chan string, 1)
			go func() { data, _ := io.ReadAll(master); captured <- string(data) }()
			inputPath := filepath.Join(dir, "stdin")
			require.NoError(t, os.WriteFile(inputPath, []byte(tc.input), 0o600))
			stdin, err := os.Open(inputPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stdin.Close() })
			stdout, err := os.Create(filepath.Join(dir, "stdout"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = stdout.Close() })
			e := task.NewExecutor(task.WithDir(dir), task.WithStderr(slave), task.WithStdout(stdout), task.WithStdin(stdin), task.WithTerminalStatus("always"), task.WithParallel(tc.parallel), task.WithAssumeTerm(true), task.WithAssumeYes(tc.assumeYes))
			require.NoError(t, e.Setup())
			calls := make([]*task.Call, len(tc.calls))
			for i, name := range tc.calls {
				calls[i] = &task.Call{Task: name}
			}
			err = e.Run(context.Background(), calls...)
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, slave.Close())
			output := <-captured
			records := rootRecords(t, output)
			require.NotEmpty(t, records)
			final := records[len(records)-1]
			require.Equal(t, tc.terminalState, final.State)
			require.Len(t, remainingRecords(t, output), 1)
			stdoutBytes, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			require.NotContains(t, string(stdoutBytes), "\x1b]7501;")
			require.NotContains(t, string(stdoutBytes), "\x1b]2;")
			title := "\x1b]2;task " + strings.Join(tc.calls, " ") + "\x07"
			require.Equal(t, 1, strings.Count(output, title))
			require.NotContains(t, output, "\x1b[22;2t")
			require.NotContains(t, output, "\x1b[23;2t")
			require.Contains(t, output, final.Sequence)
			if tc.terminalState == "done" {
				count := 0
				for _, record := range records {
					if record.State == "done" {
						count++
					}
				}
				require.Equal(t, 1, count)
			}
			if tc.input != "" {
				require.Contains(t, output, "state=blocked:")
				require.Contains(t, output, "kind=permission")
			}
			if tc.assumeYes {
				require.NotContains(t, output, "state=blocked:")
			}
			for _, marker := range []string{"slow-finished\r\n", "cleanup-finished\r\n"} {
				if index := strings.LastIndex(output, marker); index >= 0 {
					require.Greater(t, strings.LastIndex(output, final.Sequence), index)
				}
			}
		})
	}
}

func TestTerminalStatusModes(t *testing.T) { //nolint:paralleltest // changes environment
	t.Setenv("_TASK_TERMINAL_PARENT", "")
	for _, tc := range []struct {
		name, mode, term, ci string
		enabled              bool
	}{
		{"auto terminal", "auto", "xterm-256color", "", true},
		{"auto dumb", "auto", "dumb", "", false},
		{"auto CI", "auto", "xterm-256color", "true", false},
		{"always overrides", "always", "dumb", "true", true},
		{"never", "never", "xterm-256color", "", false},
		{"library default", "", "xterm-256color", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.term)
			t.Setenv("CI", tc.ci)
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(statusTaskfile), 0o600))
			master, slave, err := pty.Open()
			require.NoError(t, err)
			t.Cleanup(func() { _ = slave.Close(); _ = master.Close() })
			captured := make(chan string, 1)
			go func() { data, _ := io.ReadAll(master); captured <- string(data) }()
			e := task.NewExecutor(task.WithDir(dir), task.WithStderr(slave), task.WithStdout(io.Discard), task.WithTerminalStatus(tc.mode))
			require.NoError(t, e.Setup())
			require.NoError(t, e.Run(context.Background(), &task.Call{Task: "ok"}))
			require.NoError(t, slave.Close())
			output := <-captured
			require.Equal(t, tc.enabled, strings.Contains(output, "\x1b]7501;"))
			require.Equal(t, tc.enabled, strings.Contains(output, "\x1b]2;"))
		})
	}
}
