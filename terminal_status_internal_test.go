package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/go-task/task/v3/errors"
	"github.com/go-task/task/v3/internal/input"
	"github.com/go-task/task/v3/internal/logger"
)

func TestTerminalOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		state   ansi.ProgramState
		message string
	}{
		{"success", nil, ansi.ProgramStateDone, "Tasks finished"},
		{"failure", errors.New("secret command or error detail"), ansi.ProgramStateError, "Task failed"},
		{"timeout", context.DeadlineExceeded, ansi.ProgramStateError, "Task failed"},
		{"context cancellation", fmt.Errorf("wrapped: %w", context.Canceled), ansi.ProgramStateIdle, "Cancelled"},
		{"input cancellation", input.ErrCancelled, ansi.ProgramStateIdle, "Cancelled"},
		{"confirmation declined", logger.ErrPromptCancelled, ansi.ProgramStateIdle, "Cancelled"},
		{"task cancellation", fmt.Errorf("wrapped: %w", &errors.TaskCancelledByUserError{TaskName: "deploy"}), ansi.ProgramStateIdle, "Cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, message := terminalOutcome(tc.err)
			require.Equal(t, tc.state, state)
			require.Equal(t, tc.message, message)
		})
	}
}
