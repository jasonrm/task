package ast_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/go-task/task/v3/internal/hash"
	"github.com/go-task/task/v3/taskfile/ast"
)

func TestTerminalStatusPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		yaml  string
		valid bool
	}{
		{"{}", true},
		{"{terminal_status: {}}", true},
		{"{terminal_status: {suppress: []}}", true},
		{"{terminal_status: {suppress: [working, blocked, done, error, idle]}}", true},
		{"{terminal_status: {suppress: [clear]}}", false},
		{"{terminal_status: {suppress: [Working]}}", false},
		{"{terminal_status: {suppress: [working, working]}}", false},
		{"{terminal_status: {suppress: working}}", false},
		{"{terminal_status: {suppress: null}}", false},
		{"{terminal_status: {suppress: [true]}}", false},
		{"{terminal_status: {suppres: [working]}}", false},
		{"{terminal_status: [working]}", false},
		{"{terminal_status: false}", false},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			t.Parallel()
			var task ast.Task
			err := yaml.Unmarshal([]byte(tc.yaml), &task)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestTerminalStatusDoesNotChangeExecutionHash(t *testing.T) {
	t.Parallel()
	task := &ast.Task{Task: "dev", FullName: "dev", Location: &ast.Location{Taskfile: "Taskfile.yml"}}
	before, err := hash.Hash(task)
	require.NoError(t, err)
	task.TerminalStatus.Suppress = []string{"working"}
	after, err := hash.Hash(task)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestTerminalStatusDeepCopy(t *testing.T) {
	t.Parallel()
	var task ast.Task
	require.NoError(t, yaml.Unmarshal([]byte("terminal_status: {suppress: [working]}"), &task))
	copied := task.DeepCopy()
	require.Equal(t, []string{"working"}, copied.TerminalStatus.Suppress)
	copied.TerminalStatus.Suppress[0] = "done"
	require.Equal(t, []string{"working"}, task.TerminalStatus.Suppress)
}
