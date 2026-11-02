package terminalstatus

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// SetTitle publishes the invocation's title once at the Run boundary, not for
// dependencies or watch cycles. OSC 2 is a one-way hint: the shell sets its own
// title at the next prompt. We neither query nor guess the previous title.
func (a *Activity) SetTitle() {
	a.update(func() {
		_, _ = io.WriteString(a.reporter.out, ansi.SetWindowTitle(safeTitle(a.report.Title)))
	})
}

// OSC 2 is not base64 encoded. Strip control and formatting characters before
// writing, and bound bytes without splitting UTF-8. Task names are untrusted.
func safeTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			r = ' '
		}
		if b.Len()+utf8.RuneLen(r) > 192 {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
