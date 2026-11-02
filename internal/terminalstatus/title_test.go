package terminalstatus

import (
	"bytes"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestTitleIsIndependentOfWatchActivity(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	_, invocation := r.Start(t.Context(), "task build test", "Running")
	invocation.SetTitle()
	for range 2 {
		_, cycle := r.Start(t.Context(), "task test", "Running tests")
		cycle.Finish(ansi.ProgramStateIdle, "Watching for changes")
	}
	require.Equal(t, 1, strings.Count(b.String(), ansi.SetWindowTitle("task build test")))
	require.Equal(t, 1, strings.Count(b.String(), "\x1b]2;"))
	require.NotContains(t, b.String(), "\x1b[22;2t")
	require.NotContains(t, b.String(), "\x1b[23;2t")
	require.NotContains(t, b.String(), "state=clear")
}

func TestExitDoesNotOverwriteShellTitle(t *testing.T) {
	t.Parallel()
	for _, state := range []ansi.ProgramState{ansi.ProgramStateDone, ansi.ProgramStateError, ansi.ProgramStateIdle} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			r := newReporter(&b)
			_, a := r.Start(t.Context(), "task build", "Running")
			a.SetTitle()
			a.Finish(state, "Finished")
			// Simulate the shell reclaiming its title after Task returns.
			shell := ansi.SetWindowTitle("shell — project")
			b.WriteString(shell)
			n := b.Len()
			a.SetTitle()
			a.Finish(ansi.ProgramStateDone, "Late completion")
			require.Equal(t, n, b.Len())
			r.Cancel()
			a.SetTitle()
			require.Equal(t, 2, strings.Count(b.String(), "\x1b]2;"))
			require.NotContains(t, b.String(), "\x1b[22;2t")
			require.NotContains(t, b.String(), "\x1b[23;2t")
		})
	}
}

func TestRetiredActivityCannotSetTitle(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := newReporter(&b)
	_, old := r.Start(t.Context(), "old", "Running")
	_, current := r.Start(t.Context(), "current", "Running")
	old.SetTitle()
	current.Finish(ansi.ProgramStateDone, "Finished")
	current.SetTitle()
	var disabled *Activity
	disabled.SetTitle()
	require.NotContains(t, b.String(), "\x1b]2;")
}

func TestSafeTitle(t *testing.T) {
	t.Parallel()
	for _, title := range []string{
		"task build\x1b]2;injected\a\r\n\t\u009c\u202e",
		strings.Repeat("界", 200),
		"task \xff test",
	} {
		safe := safeTitle(title)
		require.True(t, utf8.ValidString(safe))
		require.LessOrEqual(t, len(safe), 192)
		for _, r := range safe {
			require.False(t, unicode.IsControl(r) || unicode.Is(unicode.Cf, r))
		}
	}
	require.Equal(t, "task build test", safeTitle("task build test"))
}

func TestChildEnvironmentDoesNotMutateCaller(t *testing.T) {
	t.Parallel()
	original := make([]string, 1, 8)
	original[0] = "FOO=bar"
	child := ChildEnv(original)
	require.Equal(t, []string{"FOO=bar", parentEnv + "=1"}, child)
	require.Equal(t, []string{"FOO=bar"}, original)
	require.Empty(t, original[:cap(original)][1])
}
