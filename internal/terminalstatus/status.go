// Package terminalstatus reports scoped activities without owning terminal input
// or rendering command output. Reporting is best effort.
package terminalstatus

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/go-task/task/v3/errors"
)

// External Task processes cannot safely share the in-process activity tree.
// Pass ownership only in child environments, never process-wide.
const parentEnv = "_TASK_TERMINAL_PARENT"

// ChildEnv marks shell commands as descendants without modifying the caller's slice.
func ChildEnv(environ []string) []string {
	return append(slices.Clone(environ), parentEnv+"=1")
}

type activityKey struct{}

var nextInvocation atomic.Uint64

// Reporter owns one root summary and an invocation-local namespace of task
// records. One lock serializes record updates, aggregation, and retirement.
type Reporter struct {
	mu      sync.Mutex
	out     io.Writer
	current *Activity
	stopped bool
}

// Activity is a scoped task call. Only activities belonging to the current root
// can publish. All methods accept nil, so disabled reporting needs no branches.
type Activity struct {
	reporter     *Reporter
	root, parent *Activity
	report       ansi.ProgramStatus
	waits        []*ansi.ProgramStatus
	suppress     []string
	finished     bool
	children     int
	last         string
	results      []result
	// Successful calls need only aggregate counts, never retained activity trees.
	completed, completedDone int

	// Only the root owns these. IDs are flat beneath a private namespace, so deep
	// task graphs cannot exceed the protocol's ID depth or length limits.
	namespace string
	nextChild uint64
	members   []*Activity
}

// Outcome carries Task's interpretation of an error. Err is used only to match
// propagated outcomes; neither its text nor command output enters the protocol.
type Outcome struct {
	State   ansi.ProgramState
	Message string
	Err     error
	// Cause preserves timeout provenance when callers replace cancellation errors.
	Cause error
}

type result struct {
	call   *Activity
	err    error
	cause  error
	report ansi.ProgramStatus
}

func (r result) matches(err error) bool {
	return err != nil && (r.err != nil && errors.Is(err, r.err) || r.cause != nil && errors.Is(err, r.cause))
}

// Ignore retires a child failure that Task has explicitly handled. Leaving it
// alive would make a long-running parent look failed despite ignore_error.
func Ignore(ctx context.Context, err error) {
	a, _ := ctx.Value(activityKey{}).(*Activity)
	a.update(func() {
		a.results = slices.DeleteFunc(a.results, func(previous result) bool {
			if !previous.matches(err) {
				return false
			}
			a.root.members = slices.DeleteFunc(a.root.members, func(member *Activity) bool {
				for scope := member; scope != nil; scope = scope.parent {
					if scope == previous.call {
						a.reporter.clear(member.report.ID)
						return true
					}
				}
				return false
			})
			return true
		})
	})
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

func newReporter(out io.Writer) *Reporter { return &Reporter{out: out} }

// Start replaces the entire invocation or watch cycle, retiring its callbacks
// and clearing only its own namespace. It never clears the terminal's root.
func (r *Reporter) Start(ctx context.Context, title, message string) (context.Context, *Activity) {
	var a *Activity
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.stopped {
			if r.current != nil {
				r.clearChildren(r.current)
			}
			a = &Activity{reporter: r, report: ansi.ProgramStatus{
				State: ansi.ProgramStateWorking, App: "task", Title: title, Message: message,
			}}
			a.root = a
			r.current = a
			r.render()
		}
	}
	// Mask outer activities even when this executor has reporting disabled.
	return context.WithValue(ctx, activityKey{}, a), a
}

// StartTask reports an individual task call. Policy is local, not inherited.
// Parents with active child calls are waiting, not independently working.
func StartTask(ctx context.Context, title string, suppress []string) (context.Context, *Activity) {
	parent, _ := ctx.Value(activityKey{}).(*Activity)
	if parent == nil {
		return ctx, nil
	}
	var a *Activity
	parent.update(func() {
		root := parent.root
		if root.namespace == "" {
			root.namespace = fmt.Sprintf("task-%x-%x", os.Getpid(), nextInvocation.Add(1))
		}
		root.nextChild++
		a = &Activity{reporter: parent.reporter, root: root, parent: parent, suppress: slices.Clone(suppress), report: ansi.ProgramStatus{
			ID:  fmt.Sprintf("%s/%x", root.namespace, root.nextChild),
			App: "task", Title: title, State: ansi.ProgramStateWorking, Message: "Running",
		}}
		parent.children++
		root.members = append(root.members, a)
	})
	return context.WithValue(ctx, activityKey{}, a), a
}

// Wait reports a Task-owned input wait. Overlapping waits are released
// independently; the oldest outstanding wait on an activity wins.
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

func (a *Activity) Finish(state ansi.ProgramState, message string) {
	a.FinishOutcome(Outcome{State: state, Message: message})
}

// FinishOutcome retires a call after its deferred cleanup. Successful children
// disappear; failures remain until the root publishes its final outcome. Matching
// propagated errors preserves the failing child's policy, not its parent's.
func (a *Activity) FinishOutcome(outcome Outcome) {
	a.update(func() {
		a.report.State, a.report.Message = outcome.State, outcome.Message
		a.waits = nil
		a.finished = true
		effective := a.visible()
		inherited := false
		if outcome.Err != nil {
			for _, previous := range a.results {
				if previous.matches(outcome.Err) {
					effective, inherited = previous.report, true
					break
				}
			}
		}
		// Parallel groups return one error. Never let a suppressed failure hide
		// another child's unsuppressed failure, including inside nested groups.
		if outcome.State == ansi.ProgramStateError {
			for _, previous := range a.results {
				if previous.report.State == ansi.ProgramStateError {
					effective, inherited = previous.report, true
					break
				}
			}
		}
		if a.parent == nil {
			if inherited {
				a.report.State, a.report.Message = effective.State, effective.Message
				if effective.State == ansi.ProgramStateError {
					a.report.Message = "Task failed: " + effective.Title
				}
			} else if outcome.State == ansi.ProgramStateDone && a.completed > 0 && a.completedDone == 0 {
				a.report.State, a.report.Message = ansi.ProgramStateIdle, "Finished"
			}
			// Reaffirm completion after all work and cleanup, even if a running
			// child's failure already produced the same summary. Then retire detail.
			a.last = ""
			a.reporter.publish(a, a.report)
			a.reporter.clearChildren(a)
			return
		}
		// Ancestors need this call's resolved outcome, not its completed history.
		a.results = nil
		a.parent.children--
		a.parent.completed++
		if effective.State == ansi.ProgramStateDone {
			a.parent.completedDone++
		}
		if outcome.Err != nil || outcome.State == ansi.ProgramStateError {
			a.parent.results = append(a.parent.results, result{call: a, err: outcome.Err, cause: outcome.Cause, report: effective})
		}
		if !inherited && effective.State == ansi.ProgramStateError {
			a.reporter.publish(a, effective)
		} else {
			a.reporter.clear(a.report.ID)
			a.root.members = slices.DeleteFunc(a.root.members, func(member *Activity) bool { return member == a })
		}
	})
}

func (a *Activity) update(change func()) {
	if a == nil {
		return
	}
	r := a.reporter
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.current != a.root || a.root.finished || a.finished {
		return
	}
	change()
	r.render()
}

// Cancel retires all callbacks, including future watch restarts, before exit.
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
		a.finished = true
		r.publish(a, a.report)
		r.clearChildren(a)
	}
}

// visible maps suppressed states to neutral idle, rather than leaving a stale
// record behind or claiming success. Calls waiting on children aren't busy work.
func (a *Activity) visible() ansi.ProgramStatus {
	report := a.report
	if !a.finished && a.children > 0 {
		report.State, report.Message = ansi.ProgramStateIdle, "Waiting for tasks"
	}
	if len(a.waits) > 0 {
		report.State = ansi.ProgramStateBlocked
		report.Kind, report.Message = a.waits[0].Kind, a.waits[0].Message
	}
	if slices.Contains(a.suppress, string(report.State)) {
		report.State, report.Kind = ansi.ProgramStateIdle, ""
		report.Message = "Running"
		if a.finished {
			report.Message = "Finished"
		}
	}
	return report
}

// render publishes individual facts and an aggregate summary. Blocked outranks
// failure, then working, then idle. Final outcomes are supplied by Task, not
// inferred from historical failures that the Taskfile may have ignored.
func (r *Reporter) render() {
	root := r.current
	summary := root.report
	if !root.finished && root.namespace != "" {
		summary.State, summary.Message = ansi.ProgramStateIdle, "Running tasks"
		summary.Kind = ""
		for _, member := range root.members {
			report := member.visible()
			r.publish(member, report)
			if priority(report.State) > priority(summary.State) {
				summary.State, summary.Kind = report.State, report.Kind
				summary.Message = report.Message
				if report.State == ansi.ProgramStateError {
					summary.Message = "Task failed: " + report.Title
				}
			}
		}
	}
	if len(root.waits) > 0 {
		summary.State = ansi.ProgramStateBlocked
		summary.Kind, summary.Message = root.waits[0].Kind, root.waits[0].Message
	}
	r.publish(root, summary)
}

func priority(state ansi.ProgramState) int {
	switch state {
	case ansi.ProgramStateBlocked:
		return 3
	case ansi.ProgramStateError:
		return 2
	case ansi.ProgramStateWorking:
		return 1
	default:
		return 0
	}
}

func (r *Reporter) clearChildren(root *Activity) {
	root.results = nil
	root.completed, root.completedDone = 0, 0
	if root.namespace == "" {
		return
	}
	r.clear(root.namespace)
	root.members = nil
	root.namespace = ""
}

func (r *Reporter) clear(id string) {
	// Never address the root: clearing it erases other programs' records as well.
	if id != "" {
		_, _ = io.WriteString(r.out, ansi.SetProgramStatus(ansi.ProgramStatus{State: ansi.ProgramStateClear, ID: id, App: "task"}))
	}
}

// Charm bounds and sanitizes protocol text. Each record has its own dedup cache;
// complete sequences are written under the same lock, separately from task logs.
func (r *Reporter) publish(a *Activity, report ansi.ProgramStatus) {
	sequence := ansi.SetProgramStatus(report)
	if sequence == a.last {
		return
	}
	a.last = sequence
	_, _ = io.WriteString(r.out, sequence)
}
