// Package logging gives the binary one place to configure run-time logging and
// gives every library package a logger that is silent until it does.
//
// Two rules shape this package.
//
// Logs go to stderr, never stdout. "disco run --json" exists so the campaign
// config can be piped into jq, and a single log line on stdout corrupts that
// stream. Nothing here ever writes to stdout; the human-readable campaign
// summary and the JSON config are the only things that do.
//
// Library packages call L(), not slog.Default(). The default slog logger
// writes to stderr at INFO, so a package reaching for it would make every
// "go test ./..." run print pipeline chatter. L() starts as a discard logger
// and only becomes real when a main() calls Set — which is the same reason
// library code should not log to a global it did not install.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// current is swapped atomically because stage 5 fires one model call per
// persona concurrently, and each of those goroutines logs.
var current atomic.Pointer[slog.Logger]

func init() { current.Store(slog.New(discardHandler{})) }

// L returns the process logger. Before Set is called it discards everything,
// so importing this package costs a library nothing.
func L() *slog.Logger { return current.Load() }

// Set installs the process logger. Call it once, from main, after flag parsing.
func Set(l *slog.Logger) { current.Store(l) }

// New returns a logger writing compact, human-scannable lines to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(&cliHandler{w: w, level: level, mu: &sync.Mutex{}})
}

// discardHandler drops every record. slog.DiscardHandler landed in Go 1.24;
// this keeps the package readable without depending on which minor version
// the reader has.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// cliHandler renders a record as one aligned line:
//
//	14:22:07 > calling model        stage=fit model=gemini-3.5-flash-lite
//	14:22:19 ! rate limited         stage=fit attempt=1 wait=2s
//
// slog.TextHandler was the alternative. It prints a full RFC3339 timestamp and
// quotes every message, which reads fine in a log aggregator and badly in a
// terminal a reviewer is watching for thirty seconds — which is the only place
// this output goes.
type cliHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

// msgWidth is the column the attributes start at, so the eye can run down the
// key=value pairs instead of hunting for them at a ragged edge.
const msgWidth = 24

func (h *cliHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *cliHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(marker(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	for n := len(r.Message); n < msgWidth; n++ {
		b.WriteByte(' ')
	}
	for _, a := range h.attrs {
		writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *cliHandler) WithAttrs(as []slog.Attr) slog.Handler {
	// Copy rather than append in place: append may share the backing array
	// with the parent handler, and two concurrent stage-5 goroutines each
	// deriving a logger would then write over each other's attributes.
	merged := make([]slog.Attr, 0, len(h.attrs)+len(as))
	merged = append(merged, h.attrs...)
	merged = append(merged, as...)
	return &cliHandler{mu: h.mu, w: h.w, level: h.level, attrs: merged}
}

// WithGroup returns the handler unchanged: nothing in this binary logs grouped
// attributes, and a half-implemented group would be worse than none.
func (h *cliHandler) WithGroup(string) slog.Handler { return h }

func writeAttr(b *strings.Builder, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	// One record is one line. Errors and model-written prose both carry
	// newlines — the chain's missing-fixture error is three paragraphs — and
	// a record that spans lines cannot be grepped or read in a column.
	b.WriteString(oneLine(a.Value.String()))
}

var newlines = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

func oneLine(s string) string { return strings.TrimSpace(newlines.Replace(s)) }

func marker(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "x"
	case l >= slog.LevelWarn:
		return "!"
	case l >= slog.LevelInfo:
		return ">"
	default:
		return "."
	}
}

// Elapsed rounds a duration to something worth reading. Nobody needs to know a
// stage took 1.83271ms — but rounding everything under a second to whole
// milliseconds printed "took=0s" for a fixture replay, which reads like a bug
// rather than like speed. Each band keeps three significant figures.
func Elapsed(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(100 * time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}

// Brief shortens an advertiser brief for a log line. The full text is already
// in the campaign config; this is only here so a reader can tell two runs apart.
func Brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 48 {
		return s
	}
	return s[:47] + "…"
}
