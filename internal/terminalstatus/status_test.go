package terminalstatus

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func reports(t *testing.T, b *bytes.Buffer) []map[string]string {
	t.Helper()
	var result []map[string]string
	for _, sequence := range strings.Split(b.String(), "\x07") {
		if sequence == "" {
			continue
		}
		require.True(t, strings.HasPrefix(sequence, "\x1b]7501;"))
		require.LessOrEqual(t, len(sequence)+1, 4096)
		report := make(map[string]string)
		for _, field := range strings.Split(strings.TrimPrefix(sequence, "\x1b]7501;"), ":") {
			key, value, ok := strings.Cut(field, "=")
			require.True(t, ok)
			if key == "title" || key == "msg" {
				decoded, err := base64.StdEncoding.DecodeString(value)
				require.NoError(t, err)
				require.True(t, utf8.Valid(decoded))
				for _, r := range string(decoded) {
					require.False(t, unicode.IsControl(r))
				}
				value = string(decoded)
			}
			report[key] = value
		}
		if report["state"] == "clear" {
			require.NotEmpty(t, report["id"], "must never clear the terminal root")
		} else {
			require.Equal(t, "task", report["app"])
		}
		result = append(result, report)
	}
	return result
}

func states(t *testing.T, b *bytes.Buffer) []string {
	t.Helper()
	var result []string
	for _, report := range reports(t, b) {
		result = append(result, report["state"])
	}
	return result
}

func TestActivityOutcomes(t *testing.T) {
	t.Parallel()
	for _, state := range []ansi.ProgramState{ansi.ProgramStateDone, ansi.ProgramStateError, ansi.ProgramStateIdle} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			r := newReporter(&b)
			ctx, a := r.Start(t.Context(), "task build", "Building")
			a.Finish(state, "Result")
			// Completion retires the activity: neither waits nor a second finish revive it.
			Wait(ctx, ansi.ProgramStatusKindQuestion, "Late prompt")()
			a.Finish(ansi.ProgramStateError, "Late error")
			require.Equal(t, []string{"working", string(state)}, states(t, &b))
			for _, report := range reports(t, &b) {
				require.Equal(t, "task build", report["title"])
			}
		})
	}
}

func TestOverlappingWaits(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		var b bytes.Buffer
		r := newReporter(&b)
		ctx, a := r.Start(t.Context(), "task deploy", "Deploying")
		first := Wait(ctx, ansi.ProgramStatusKindPermission, "Approve deployment")
		second := Wait(ctx, ansi.ProgramStatusKindQuestion, "Choose environment")
		if reverse {
			second()
			first()
		} else {
			first()
			second()
		}
		first()
		second() // releases are idempotent, regardless of order
		a.Finish(ansi.ProgramStateDone, "Deployed")
		want := []string{"working", "blocked", "blocked", "working", "done"}
		if reverse {
			want = []string{"working", "blocked", "working", "done"}
		}
		require.Equal(t, want, states(t, &b))
	}
}

func TestReplacingActivityRetiresItsCallbacks(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	oldCtx, old := r.Start(t.Context(), "task test", "Running tests")
	release := Wait(oldCtx, ansi.ProgramStatusKindQuestion, "Old prompt")
	_, current := r.Start(t.Context(), "task test", "Running tests")
	n := b.Len()
	release()
	Wait(oldCtx, ansi.ProgramStatusKindPermission, "Stale prompt")()
	old.Finish(ansi.ProgramStateError, "Stale failure")
	require.Equal(t, n, b.Len())
	current.Finish(ansi.ProgramStateError, "Tests failed")
	_, next := r.Start(t.Context(), "task test", "Running tests")
	next.Finish(ansi.ProgramStateIdle, "Watching for changes")
	require.Equal(t, []string{"working", "blocked", "working", "error", "working", "idle"}, states(t, &b))
}

func TestCancellationRetiresReporter(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	ctx, a := r.Start(t.Context(), "task build", "Building")
	release := Wait(ctx, ansi.ProgramStatusKindPermission, "Approve")
	r.Cancel()
	release()
	a.Finish(ansi.ProgramStateError, "Interrupted child")
	_, late := r.Start(t.Context(), "task build", "Late restart")
	late.Finish(ansi.ProgramStateDone, "Late success")
	r.Cancel()
	require.Equal(t, []string{"working", "blocked", "idle"}, states(t, &b))
}

func TestConcurrentWaits(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	ctx, a := r.Start(t.Context(), "task build", "Building")
	var g sync.WaitGroup
	for range 100 {
		g.Go(func() {
			release := Wait(ctx, ansi.ProgramStatusKindQuestion, "Input required")
			release()
		})
	}
	g.Wait()
	a.Finish(ansi.ProgramStateDone, "Built")
	got := reports(t, &b)
	require.Equal(t, "done", got[len(got)-1]["state"])
}

func TestEncodingAndRootRecord(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	_, a := r.Start(t.Context(), strings.Repeat("界", 200)+"\x1b\x07\n", "Building")
	a.Finish(ansi.ProgramStateDone, "Built")
	got := reports(t, &b)
	require.LessOrEqual(t, len(got[0]["title"]), 192)
	require.Equal(t, got[0]["id"], got[1]["id"])
	_, next := newReporter(&b).Start(t.Context(), "task next", "Running")
	next.Finish(ansi.ProgramStateDone, "Finished")
	for _, report := range reports(t, &b) {
		require.Empty(t, report["id"])
	}
	require.NotContains(t, b.String(), "state=clear")
}

func TestDisabledReporterMasksOuterActivity(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	outer := newReporter(&b)
	ctx, a := outer.Start(t.Context(), "outer", "Working")
	var disabled *Reporter
	innerCtx, inner := disabled.Start(ctx, "disabled", "Working")
	Wait(innerCtx, ansi.ProgramStatusKindQuestion, "Must not affect outer")()
	inner.Finish(ansi.ProgramStateError, "Must not affect outer")
	disabled.Cancel()
	a.Finish(ansi.ProgramStateDone, "Finished")
	require.Equal(t, []string{"working", "done"}, states(t, &b))
}

func TestReportersCannotBorrowEachOthersActivity(t *testing.T) {
	t.Parallel()
	var first, second bytes.Buffer
	r1, r2 := newReporter(&first), newReporter(&second)
	ctx, a := r1.Start(t.Context(), "first", "Working")
	otherCtx, other := r2.Start(ctx, "second", "Working")
	Wait(otherCtx, ansi.ProgramStatusKindQuestion, "Second needs input")()
	other.Finish(ansi.ProgramStateDone, "Second finished")
	a.Finish(ansi.ProgramStateDone, "First finished")
	require.Equal(t, []string{"working", "done"}, states(t, &first))
	require.Equal(t, []string{"working", "blocked", "working", "done"}, states(t, &second))
}

func TestRedirectedOutputNeverReports(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	require.NoError(t, err)
	defer file.Close()
	for _, mode := range []string{"", "auto", "always", "never"} {
		require.Nil(t, New(file, mode))
		require.Nil(t, New(io.Discard, mode))
	}
	Wait(context.Background(), ansi.ProgramStatusKindQuestion, "No activity")()
}
