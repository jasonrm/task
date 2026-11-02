package terminalstatus

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func snapshot(t *testing.T, b *bytes.Buffer) map[string]map[string]string {
	t.Helper()
	records := map[string]map[string]string{}
	for _, report := range reports(t, b) {
		id := report["id"]
		if report["state"] == "clear" {
			for existing := range records {
				if existing == id || strings.HasPrefix(existing, id+"/") {
					delete(records, existing)
				}
			}
		} else {
			records[id] = report
		}
	}
	return records
}

func TestParallelChildrenAndQuietParent(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	ctx, root := r.Start(t.Context(), "task dev", "Running tasks")
	devCtx, dev := StartTask(ctx, "dev", []string{"working"})
	_, npm := StartTask(devCtx, "npm", nil)
	composerCtx, composer := StartTask(devCtx, "composer", nil)
	got := snapshot(t, &b)
	require.Equal(t, "working", got[""]["state"])
	require.Equal(t, "working", got[npm.report.ID]["state"])
	require.Equal(t, "working", got[composer.report.ID]["state"])
	require.Equal(t, "idle", got[dev.report.ID]["state"])
	npm.Finish(ansi.ProgramStateDone, "Finished")
	got = snapshot(t, &b)
	require.NotContains(t, got, npm.report.ID)
	require.Equal(t, "working", got[""]["state"])
	release := Wait(composerCtx, ansi.ProgramStatusKindPermission, "Approve")
	require.Equal(t, "blocked", snapshot(t, &b)[""]["state"])
	release()
	composer.Finish(ansi.ProgramStateDone, "Finished")
	got = snapshot(t, &b)
	require.Equal(t, "idle", got[""]["state"])
	require.Len(t, got, 2)
	// A dependency does not acquire its parent's suppression policy.
	_, build := StartTask(devCtx, "rebuild", nil)
	require.Equal(t, "working", snapshot(t, &b)[""]["state"])
	build.Finish(ansi.ProgramStateDone, "Finished")
	require.Equal(t, "idle", snapshot(t, &b)[""]["state"])
	dev.Finish(ansi.ProgramStateDone, "Finished")
	root.Finish(ansi.ProgramStateDone, "Finished")
	got = snapshot(t, &b)
	require.Len(t, got, 1)
	require.Equal(t, "done", got[""]["state"])
}

func TestParentWaitsForQuietChild(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task dev", "Running")
	parentCtx, parent := StartTask(ctx, "orchestrator", nil)
	_, child := StartTask(parentCtx, "server", []string{"working"})
	require.Equal(t, "idle", snapshot(t, &b)[""]["state"])
	child.Finish(ansi.ProgramStateDone, "Finished")
	require.Equal(t, "working", snapshot(t, &b)[""]["state"], "parent resumes its own work")
	parent.Finish(ansi.ProgramStateDone, "Finished")
	root.Finish(ansi.ProgramStateDone, "Finished")
}

func TestStateSuppressionAndFinalOutcome(t *testing.T) {
	t.Parallel()
	for _, state := range []ansi.ProgramState{ansi.ProgramStateWorking, ansi.ProgramStateBlocked, ansi.ProgramStateDone, ansi.ProgramStateError, ansi.ProgramStateIdle} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			ctx, root := newReporter(&b).Start(t.Context(), "task test", "Running")
			childCtx, child := StartTask(ctx, "test", []string{string(state)})
			if state == ansi.ProgramStateWorking {
				require.Equal(t, "idle", snapshot(t, &b)[""]["state"])
			} else if state == ansi.ProgramStateBlocked {
				release := Wait(childCtx, ansi.ProgramStatusKindQuestion, "Input")
				got := snapshot(t, &b)
				require.Equal(t, "idle", got[""]["state"])
				require.Empty(t, got[""]["kind"])
				require.Equal(t, "idle", got[child.report.ID]["state"])
				release()
				require.Equal(t, "working", snapshot(t, &b)[""]["state"])
			} else {
				err := errors.New("private error")
				outcome := Outcome{State: state, Message: "Result"}
				if state == ansi.ProgramStateError {
					outcome.Err = err
				}
				child.FinishOutcome(outcome)
				root.FinishOutcome(outcome)
				got := snapshot(t, &b)
				require.Len(t, got, 1)
				require.Equal(t, "idle", got[""]["state"])
				require.NotContains(t, b.String(), "private error")
			}
		})
	}
}

func TestFailurePolicyFollowsOrigin(t *testing.T) {
	t.Parallel()
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			ctx, root := newReporter(&b).Start(t.Context(), "task dev", "Running")
			parentCtx, parent := StartTask(ctx, "dev", []string{"working", "error"})
			var policy []string
			if hidden {
				policy = []string{"error"}
			}
			_, child := StartTask(parentCtx, "composer", policy)
			failure := errors.New("secret error details")
			child.FinishOutcome(Outcome{State: ansi.ProgramStateError, Message: "Task failed", Err: failure})
			got := snapshot(t, &b)
			if hidden {
				require.NotContains(t, got, child.report.ID)
			} else {
				require.Equal(t, "error", got[child.report.ID]["state"])
			}
			wrapped := fmt.Errorf("wrapped: %w", failure)
			parent.FinishOutcome(Outcome{State: ansi.ProgramStateError, Message: "Task failed", Err: wrapped})
			root.FinishOutcome(Outcome{State: ansi.ProgramStateError, Message: "Task failed", Err: wrapped})
			got = snapshot(t, &b)
			require.Len(t, got, 1)
			if hidden {
				require.Equal(t, "idle", got[""]["state"])
			} else {
				require.Equal(t, "error", got[""]["state"])
				require.Equal(t, "Task failed: composer", got[""]["msg"])
			}
			require.NotContains(t, b.String(), "secret error details")
		})
	}
}

func TestHiddenFailureCannotHideParallelVisibleFailure(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task parallel", "Running")
	_, hidden := StartTask(ctx, "hidden", []string{"error"})
	_, visible := StartTask(ctx, "visible", nil)
	first, second := errors.New("first"), errors.New("second")
	hidden.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: first})
	visible.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: second})
	root.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: first})
	require.Equal(t, "Task failed: visible", snapshot(t, &b)[""]["msg"])
}

func TestIgnoredChildFailureRetiresBeforeParentFinishes(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task dev", "Running")
	parentCtx, parent := StartTask(ctx, "dev", []string{"working"})
	_, child := StartTask(parentCtx, "optional", nil)
	err := errors.New("failed")
	child.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: err})
	require.Equal(t, "error", snapshot(t, &b)[""]["state"])
	Ignore(parentCtx, err)
	require.Equal(t, "idle", snapshot(t, &b)[""]["state"])
	require.NotContains(t, snapshot(t, &b), child.report.ID)
	// An ignored failure cannot lend its policy or title to a later failure.
	later := errors.New("later")
	parent.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: later})
	root.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: later})
	require.Equal(t, "Task failed: dev", snapshot(t, &b)[""]["msg"])
}

func TestChildNamespaceRetirement(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			r := newReporter(&b)
			ctx, root := r.Start(t.Context(), "task watch", "Running")
			childCtx, child := StartTask(ctx, "build", nil)
			release := Wait(childCtx, ansi.ProgramStatusKindQuestion, "Input")
			oldNamespace := root.namespace
			if cancel {
				r.Cancel()
			} else {
				r.Start(t.Context(), "task watch", "Running")
			}
			n := b.Len()
			release()
			child.Finish(ansi.ProgramStateError, "Stale")
			_, late := StartTask(childCtx, "stale", nil)
			require.Nil(t, late)
			require.Equal(t, n, b.Len())
			require.Len(t, snapshot(t, &b), 1)
			require.Contains(t, b.String(), "state=clear:id="+oldNamespace+"\x07")
		})
	}
}

func TestChildRecordLifetimeAndIDs(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task loop", "Running")
	ids := map[string]bool{}
	for range 100 {
		_, a := StartTask(ctx, "same task / unsafe\a\x1b", nil)
		require.NotContains(t, ids, a.report.ID)
		ids[a.report.ID] = true
		require.Regexp(t, `^task-[0-9a-f]+-[0-9a-f]+/[0-9a-f]+$`, a.report.ID)
		require.LessOrEqual(t, len(a.report.ID), 128)
		a.Finish(ansi.ProgramStateDone, "Finished")
		require.Empty(t, root.members)
		require.Empty(t, root.results, "successful calls must not retain activity history")
		require.Empty(t, a.results)
	}
	require.Equal(t, 100, root.completed)
	require.Equal(t, 100, root.completedDone)
	root.Finish(ansi.ProgramStateDone, "Finished")
	require.Empty(t, root.results)
	require.Zero(t, root.completed)
	require.Len(t, snapshot(t, &b), 1)
}

func TestIgnoredFailureRetiresWholeSubtree(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task dev", "Running")
	parentCtx, parent := StartTask(ctx, "dev", []string{"working"})
	groupCtx, group := StartTask(parentCtx, "optional", nil)
	_, first := StartTask(groupCtx, "first", nil)
	_, second := StartTask(groupCtx, "second", nil)
	failure := errors.New("failure")
	first.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: failure})
	second.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: errors.New("second failure")})
	wrapped := fmt.Errorf("optional: %w", failure)
	group.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: wrapped})
	require.Empty(t, group.results, "completed groups retain no child outcome history")
	Ignore(parentCtx, wrapped)
	got := snapshot(t, &b)
	require.Len(t, got, 2)
	require.Equal(t, "idle", got[""]["state"])
	require.Contains(t, got, parent.report.ID)
	parent.Finish(ansi.ProgramStateDone, "Finished")
	root.Finish(ansi.ProgramStateDone, "Finished")
}

func TestNestedMixedFailurePolicies(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	ctx, root := newReporter(&b).Start(t.Context(), "task dev", "Running")
	groupCtx, group := StartTask(ctx, "install", nil)
	_, hidden := StartTask(groupCtx, "hidden", []string{"error"})
	_, visible := StartTask(groupCtx, "visible", nil)
	failure := errors.New("first")
	hidden.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: failure})
	visible.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: errors.New("second")})
	wrapped := fmt.Errorf("install: %w", failure)
	group.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: wrapped})
	root.FinishOutcome(Outcome{State: ansi.ProgramStateError, Err: wrapped})
	require.Equal(t, "Task failed: visible", snapshot(t, &b)[""]["msg"])
}

func TestConcurrentChildrenAndCancellation(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	ctx, _ := r.Start(t.Context(), "task parallel", "Running")
	var g sync.WaitGroup
	for range 50 {
		g.Go(func() {
			ctx, a := StartTask(ctx, "test", nil)
			release := Wait(ctx, ansi.ProgramStatusKindQuestion, "Input")
			release()
			a.Finish(ansi.ProgramStateDone, "Finished")
		})
	}
	g.Go(r.Cancel)
	g.Wait()
	require.Len(t, snapshot(t, &b), 1)
	require.Equal(t, "idle", snapshot(t, &b)[""]["state"])
}
