// Package terminalstatus reports scoped activities without owning terminal input
// or rendering command output. Reporting is best effort.
package terminalstatus

import (
	"context"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Nested Task processes defer to the outer invocation's terminal policy. This
// marker is passed only in child environments, never set process-wide.
const parentEnv = "_TASK_TERMINAL_PARENT"

// ChildEnv marks Task's shell commands (including dynamic variables and status
// checks) as descendants, so an external `task` cannot replace its parent's title
// or root record. The caller's environment slice is not modified.
func ChildEnv(environ []string) []string {
	return append(slices.Clone(environ), parentEnv+"=1")
}

type activityKey struct{}

// Reporter owns the root status record for the outer Task invocation. Repeated
// invocations replace that record instead of accumulating completed child records.
// Only the current activity can update it; stale callbacks are retired.
type Reporter struct {
	mu      sync.Mutex
	out     io.Writer
	current *Activity
	stopped bool
	last    string
}

// Activity is a lease on a reporter. Finish retires the lease, leaving its final
// status visible. All methods accept nil, so disabled reporting needs no branches.
type Activity struct {
	reporter *Reporter
	report   ansi.ProgramStatus
	waits    []*ansi.ProgramStatus
	finished bool
}

// New reports only to terminal stderr. "always" overrides CI and TERM=dumb,
// never redirection. Library callers opt in; an empty mode disables reporting.
func New(out io.Writer, mode string) *Reporter {
	if os.Getenv(parentEnv) != "" {
		return nil
	}
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	switch mode {
	case "always":
	case "auto":
		if os.Getenv("TERM") == "dumb" || os.Getenv("CI") != "" && os.Getenv("CI") != "false" {
			return nil
		}
	default:
		return nil
	}
	return newReporter(out)
}

func newReporter(out io.Writer) *Reporter {
	return &Reporter{out: out}
}

// Start replaces the previous activity. The returned context lets nested
// operations borrow the activity for input waits without knowing its reporter.
func (r *Reporter) Start(ctx context.Context, title, message string) (context.Context, *Activity) {
	var a *Activity
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.stopped {
			a = &Activity{reporter: r, report: ansi.ProgramStatus{
				State: ansi.ProgramStateWorking, App: "task", Title: title, Message: message,
			}}
			r.current = a
			r.emit(a)
		}
	}
	// Mask any outer activity even when reporting is disabled for this executor.
	return context.WithValue(ctx, activityKey{}, a), a
}

// Wait reports a Task-owned input wait until the returned function is called.
// Overlapping waits are released independently; the oldest outstanding wait wins.
func Wait(ctx context.Context, kind ansi.ProgramStatusKind, message string) func() {
	a, _ := ctx.Value(activityKey{}).(*Activity)
	wait := &ansi.ProgramStatus{Kind: kind, Message: message}
	a.update(func() { a.waits = append(a.waits, wait) })
	return func() {
		a.update(func() {
			a.waits = slices.DeleteFunc(a.waits, func(p *ansi.ProgramStatus) bool { return p == wait })
		})
	}
}

// Finish reports an outcome exactly once. Late waits and completions are inert.
func (a *Activity) Finish(state ansi.ProgramState, message string) {
	a.update(func() {
		a.report.State, a.report.Message = state, message
		a.waits = nil
		a.finished = true
	})
}

func (a *Activity) update(change func()) {
	if a == nil {
		return
	}
	r := a.reporter
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.current != a || a.finished {
		return
	}
	change()
	r.emit(a)
}

// Cancel retires the reporter, including future watch restarts, before exit.
func (r *Reporter) Cancel() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.stopped = true
	if a := r.current; a != nil {
		a.report.State, a.report.Message = ansi.ProgramStateIdle, "Cancelled"
		a.waits = nil
		r.emit(a)
	}
}

// Called with the reporter locked. Charm bounds and sanitizes UTF-8 text and
// encodes it; the entire sequence is written at once, separately from task logs.
func (r *Reporter) emit(a *Activity) {
	report := a.report
	if len(a.waits) > 0 {
		report.State = ansi.ProgramStateBlocked
		report.Kind, report.Message = a.waits[0].Kind, a.waits[0].Message
	}
	sequence := ansi.SetProgramStatus(report)
	if sequence == r.last {
		return
	}
	r.last = sequence
	_, _ = io.WriteString(r.out, sequence)
}
