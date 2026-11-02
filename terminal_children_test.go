//go:build darwin || linux

package task_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/go-task/task/v3"
)

type terminalCapture struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *terminalCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *terminalCapture) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type terminalHarness struct {
	executor *task.Executor
	dir      string
	capture  *terminalCapture
	slave    *os.File
	readDone chan struct{}
}

func newTerminalHarness(t *testing.T, source string, options ...task.ExecutorOption) *terminalHarness {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(source), 0o600))
	master, slave, err := pty.Open()
	require.NoError(t, err)
	h := &terminalHarness{dir: dir, capture: &terminalCapture{}, slave: slave, readDone: make(chan struct{})}
	t.Cleanup(func() { _ = slave.Close(); _ = master.Close(); <-h.readDone })
	go func() { _, _ = io.Copy(h.capture, master); close(h.readDone) }()
	inputPath := filepath.Join(dir, "stdin")
	require.NoError(t, os.WriteFile(inputPath, []byte("n\n"), 0o600))
	stdin, err := os.Open(inputPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	opts := []task.ExecutorOption{task.WithDir(dir), task.WithStderr(slave), task.WithStdout(io.Discard), task.WithStdin(stdin), task.WithTerminalStatus("always")}
	h.executor = task.NewExecutor(append(opts, options...)...)
	require.NoError(t, h.executor.Setup())
	return h
}

func (h *terminalHarness) finish(t *testing.T) string {
	t.Helper()
	require.NoError(t, h.slave.Close())
	select {
	case <-h.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal capture did not finish")
	}
	return h.capture.snapshot()
}

func TestTerminalChildrenParallelStartup(t *testing.T) { //nolint:paralleltest // isolates inherited ownership
	t.Setenv("_TASK_TERMINAL_PARENT", "")
	h := newTerminalHarness(t, `version: '3'
tasks:
  dev:
    terminal_status: {suppress: [working]}
    deps: [npm, composer]
    cmds: ['echo server-started >&2; while true; do sleep 0.02; done']
  npm:
    cmds: ['echo npm-started >&2; while [ ! -f npm-release ]; do sleep 0.02; done']
  composer:
    cmds: ['echo composer-started >&2; while [ ! -f composer-release ]; do sleep 0.02; done']
`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { runErr = h.executor.Run(ctx, &task.Call{Task: "dev"}); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("task did not stop")
		}
	})
	require.Eventually(t, func() bool {
		records := remainingRecords(t, h.capture.snapshot())
		titles := map[string]string{}
		for _, r := range records {
			titles[r.Title] = r.State
		}
		return titles["npm"] == "working" && titles["composer"] == "working" && records[""].State == "working"
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(h.dir, "npm-release"), nil, 0o600))
	require.Eventually(t, func() bool {
		records := remainingRecords(t, h.capture.snapshot())
		npm, composer := false, false
		for _, r := range records {
			npm = npm || r.Title == "npm"
			composer = composer || r.Title == "composer" && r.State == "working"
		}
		return !npm && composer && records[""].State == "working"
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(h.dir, "composer-release"), nil, 0o600))
	require.Eventually(t, func() bool {
		output := h.capture.snapshot()
		records := remainingRecords(t, output)
		return strings.Contains(output, "server-started\r\n") && len(records) == 2 && records[""].State == "idle"
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not stop")
	}
	require.Error(t, runErr)
	output := h.finish(t)
	records := remainingRecords(t, output)
	require.Len(t, records, 1)
	require.Equal(t, "idle", records[""].State)
	require.Equal(t, 1, strings.Count(output, "\x1b]2;"))
}

func TestTerminalChildrenOutcomes(t *testing.T) { //nolint:paralleltest // isolates inherited ownership
	t.Setenv("_TASK_TERMINAL_PARENT", "")
	for _, tc := range []struct {
		name, source, state, message string
		fail                         bool
	}{
		{"quiet failure", `  dev:
    terminal_status: {suppress: [working]}
    cmds: ['exit 7']`, "error", "Task failed: dev", true},
		{"hidden failure", `  dev:
    terminal_status: {suppress: [working, error]}
    cmds: ['exit 7']`, "idle", "Finished", true},
		{"hidden success", `  dev:
    terminal_status: {suppress: [working, done]}
    cmds: ['true']`, "idle", "Finished", false},
		{"dependency failure is not inherited", `  dev:
    terminal_status: {suppress: [working, error]}
    deps: [install]
  install:
    cmds: ['exit 7']`, "error", "Task failed: install", true},
		{"dependency hides its failure", `  dev:
    deps: [install]
  install:
    terminal_status: {suppress: [error]}
    cmds: ['exit 7']`, "idle", "Finished", true},
		{"ignored child failure", `  dev:
    terminal_status: {suppress: [working]}
    cmds:
      - task: optional
        ignore_error: true
      - 'echo after-ignore >&2'
  optional:
    cmds: ['exit 7']`, "done", "Tasks finished", false},
		{"deferred child failure", `  dev:
    cmds:
      - defer: {task: cleanup}
      - 'true'
  cleanup:
    cmds: ['exit 7']`, "done", "Tasks finished", false},
		{"suppressed timeout", `  dev:
    deps: [{task: slow, timeout: 20ms}]
  slow:
    terminal_status: {suppress: [error]}
    cmds: ['sleep 5']`, "idle", "Finished", true},
		{"visible timeout", `  dev:
    terminal_status: {suppress: [error]}
    deps: [{task: slow, timeout: 20ms}]
  slow:
    cmds: ['sleep 5']`, "error", "Task failed: slow", true},
		{"quiet prompt", `  dev:
    terminal_status: {suppress: [working]}
    prompt: 'secret confirmation'
    cmds: ['true']`, "idle", "Cancelled", true},
		{"hidden prompt", `  dev:
    terminal_status: {suppress: [working, blocked]}
    prompt: 'secret confirmation'
    cmds: ['true']`, "idle", "Cancelled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTerminalHarness(t, "version: '3'\ntasks:\n"+tc.source+"\n", task.WithAssumeTerm(true))
			err := h.executor.Run(t.Context(), &task.Call{Task: "dev"})
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			output := h.finish(t)
			records := remainingRecords(t, output)
			require.Len(t, records, 1)
			require.Equal(t, tc.state, records[""].State)
			require.Equal(t, tc.message, records[""].Message)
			blocked := false
			for _, record := range terminalRecords(t, output) {
				blocked = blocked || record.State == "blocked"
				require.NotContains(t, record.Message, "secret confirmation")
			}
			require.Equal(t, tc.name == "quiet prompt", blocked)
		})
	}
}
