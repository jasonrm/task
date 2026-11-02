//go:build darwin || linux

package task_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/go-task/task/v3"
)

// TestTerminalTitleChild is invoked as a real child process from both a dynamic
// variable and a command. Even explicit "always" must respect outer ownership.
func TestTerminalTitleChild(t *testing.T) {
	if os.Getenv("_TASK_TITLE_TEST_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	require.Equal(t, "1", os.Getenv("_TASK_TERMINAL_PARENT"))
	e := task.NewExecutor(task.WithTerminalStatus("always"))
	require.NoError(t, e.Setup())
	require.NoError(t, e.Run(t.Context(), &task.Call{Task: "child"}))
}

func TestTerminalTitleNestedProcesses(t *testing.T) { //nolint:paralleltest // isolates inherited terminal ownership
	t.Setenv("_TASK_TERMINAL_PARENT", "")
	dir := t.TempDir()
	taskfile := `version: '3'
env:
  _TASK_TITLE_TEST_CHILD: '1'
tasks:
  outer:
    vars:
      VALUE:
        sh: '"{{.TASK_EXE}}" -test.run=^TestTerminalTitleChild$'
    cmds:
      - 'echo "{{.VALUE}}"'
      - '"{{.TASK_EXE}}" -test.run=^TestTerminalTitleChild$'
      - task: child
  child:
    cmds: ['echo child-finished >&2']
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(taskfile), 0o600))
	master, slave, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = slave.Close(); _ = master.Close() })
	captured := make(chan string, 1)
	go func() { data, _ := io.ReadAll(master); captured <- string(data) }()
	e := task.NewExecutor(task.WithDir(dir), task.WithStderr(slave), task.WithStdout(io.Discard), task.WithTerminalStatus("always"))
	require.NoError(t, e.Setup())
	// Repeated invocations in the same terminal must reuse the root record.
	for range 2 {
		require.NoError(t, e.Run(t.Context(), &task.Call{Task: "outer"}))
	}
	require.NoError(t, slave.Close())
	output := <-captured
	require.NotContains(t, output, ":id=")
	require.Equal(t, 2, strings.Count(output, "state=done:"))
	require.Equal(t, 2, strings.Count(output, "\x1b]2;task outer\x07"))
	require.Equal(t, 2, strings.Count(output, "\x1b]2;"))
	require.NotContains(t, output, "\x1b[22;2t")
	require.NotContains(t, output, "\x1b[23;2t")
	require.NotContains(t, output, "state=clear")
}
