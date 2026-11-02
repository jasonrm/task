package task

import (
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/go-task/task/v3/errors"
	"github.com/go-task/task/v3/internal/input"
	"github.com/go-task/task/v3/internal/logger"
	"github.com/go-task/task/v3/internal/term"
	"github.com/go-task/task/v3/internal/terminalstatus"
)

// WithTerminalStatus enables OSC 7501 status and OSC 2 invocation titles on
// terminal stderr. Values are auto,
// always and never. Library callers default to never. Always overrides CI and
// TERM=dumb suppression, but never writes escape sequences to redirected output.
// External nested Task processes leave title and status ownership to the parent.
func WithTerminalStatus(mode string) ExecutorOption {
	return terminalStatusOption(mode)
}

type terminalStatusOption string

func (o terminalStatusOption) ApplyToExecutor(e *Executor) {
	e.TerminalStatus = string(o)
}

// Keep prompt reporting outside the UI. Neither the logger nor Bubble Tea owns
// execution status, and interpolated confirmation text may contain secrets.
func (e *Executor) prompt(ctx context.Context, message, prompt string) error {
	if !e.AssumeYes && (e.AssumeTerm || term.IsTerminal()) {
		defer terminalstatus.Wait(ctx, ansi.ProgramStatusKindPermission, message)()
	}
	return e.Logger.Prompt(logger.Yellow, prompt, "n", "y", "yes")
}

func promptVariable(ctx context.Context, p *input.Prompter, name string, values []string) (string, error) {
	defer terminalstatus.Wait(ctx, ansi.ProgramStatusKindQuestion, "Enter required variable")()
	return p.Prompt(name, values)
}

func (e *Executor) beginTerminalStatus(ctx context.Context, calls []*Call) (context.Context, *terminalstatus.Activity) {
	var reporter *terminalstatus.Reporter
	if !e.Dry && !e.Summary {
		reporter = e.terminalStatus
	}
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Task
	}
	return reporter.Start(ctx, "task "+strings.Join(names, " "), "Running tasks")
}

// Error semantics belong to Task, not to the terminal protocol adapter.
func terminalOutcome(err error) (ansi.ProgramState, string) {
	state, message := ansi.ProgramStateDone, "Tasks finished"
	var cancelled *errors.TaskCancelledByUserError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, input.ErrCancelled), errors.Is(err, logger.ErrPromptCancelled), errors.As(err, &cancelled):
		state, message = ansi.ProgramStateIdle, "Cancelled"
	case err != nil:
		state, message = ansi.ProgramStateError, "Task failed"
	}
	return state, message
}
