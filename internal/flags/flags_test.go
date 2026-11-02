package flags_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/internal/flags"
)

func TestTerminalStatusFlags(t *testing.T) { //nolint:paralleltest // mutates package state
	originalMode := flags.TerminalStatus
	originalList, originalListAll, originalListJSON := flags.List, flags.ListAll, flags.ListJson
	originalSummary, originalDry, originalStatus, originalClear := flags.Summary, flags.Dry, flags.Status, flags.ClearCache
	t.Cleanup(func() {
		flags.TerminalStatus = originalMode
		flags.List, flags.ListAll, flags.ListJson = originalList, originalListAll, originalListJSON
		flags.Summary, flags.Dry, flags.Status, flags.ClearCache = originalSummary, originalDry, originalStatus, originalClear
	})
	flags.TerminalStatus = "always"
	flags.List, flags.ListAll, flags.ListJson = false, false, false
	flags.Summary, flags.Dry, flags.Status, flags.ClearCache = false, false, false, false
	require.Equal(t, "always", task.NewExecutor(flags.WithFlags()).TerminalStatus)
	for _, flag := range []*bool{&flags.List, &flags.ListAll, &flags.ListJson, &flags.Summary, &flags.Dry, &flags.Status, &flags.ClearCache} {
		*flag = true
		require.Equal(t, "never", task.NewExecutor(flags.WithFlags()).TerminalStatus)
		*flag = false
	}
	for _, mode := range []string{"auto", "always", "never"} {
		flags.TerminalStatus = mode
		require.NoError(t, flags.Validate())
	}
	flags.TerminalStatus = "sometimes"
	require.ErrorContains(t, flags.Validate(), "--terminal-status")
}

// WithFlags applies WithTaskSorter after NewExecutor set its default, so an
// unset --sort must still resolve to a sorter instead of clearing it.
func TestWithFlags_DefaultSorterIsNotCleared(t *testing.T) { //nolint:paralleltest // mutates package state
	original := flags.TaskSort
	t.Cleanup(func() { flags.TaskSort = original })

	for _, sort := range []string{"", "default"} {
		flags.TaskSort = sort
		e := task.NewExecutor(flags.WithFlags())
		require.NotNilf(t, e.TaskSorter, "--sort %q left the executor without a sorter", sort)
	}
}
