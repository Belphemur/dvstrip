// Package display renders one progress bar per in-flight conversion at the
// bottom of the terminal. It is a thin wrapper over mpb, which natively
// supports multiple concurrent bars.
//
// Output depends on whether the destination is a terminal:
//   - TTY: mpb draws one line per bar and redraws it in place; log lines go
//     through mpb so they print above the bars without interleaving.
//   - non-TTY (docker logs, redirected stderr, CI): mpb renders nothing (no
//     ANSI codes, no per-redraw line spam). Instead the Tracker emits a log
//     line each time a determinate bar crosses a 10% milestone, so progress
//     stays visible in captured logs. Log lines bypass mpb and go straight to
//     the output (mpb never flushes its buffer without a refresh tick).
package display

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const redrawInterval = 120 * time.Millisecond

// milestonePct is the percent granularity of non-TTY progress logs: a line is
// emitted each time a bar crosses the next multiple of milestonePct.
const milestonePct = 10

// entry is a registered bar plus its non-TTY milestone bookkeeping.
type entry struct {
	bar     *mpb.Bar
	label   string
	total   int64
	nextPct int // next milestone percent not yet logged (non-TTY only)
}

// Tracker owns the terminal: workers register/unregister bars by key (file
// path). On a TTY mpb redraws the bar block in place; on a non-TTY the
// Tracker logs milestone progress lines instead.
type Tracker struct {
	p   *mpb.Progress
	out io.Writer
	tty bool
	mu  sync.Mutex
	bar map[string]*entry
}

// New starts a Tracker rendering to out (normally os.Stderr). When out is not
// a terminal, bars don't render and milestone log lines are emitted instead.
func New(out io.Writer) *Tracker {
	tty := false
	if f, ok := out.(*os.File); ok {
		tty = isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
	}
	return &Tracker{
		p:   mpb.New(mpb.WithOutput(out), mpb.WithRefreshRate(redrawInterval)),
		out: out,
		tty: tty,
		bar: make(map[string]*entry),
	}
}

// Close shuts the renderer down: every registered bar is dropped and the
// container is stopped (Shutdown — not Wait — so bars that never completed
// don't block). Log lines written through Writer after Close are discarded.
func (t *Tracker) Close() {
	t.mu.Lock()
	for k, e := range t.bar {
		e.bar.Abort(true)
		delete(t.bar, k)
	}
	t.mu.Unlock()
	t.p.Shutdown()
}

// Writer returns the writer log output must go through: on a TTY each write
// is printed above the running bars; on a non-TTY it goes straight to the
// output. Writes after Close are acknowledged but discarded so zerolog never
// sees an error racing shutdown.
func (t *Tracker) Writer() io.Writer { return writer{t} }

type writer struct{ t *Tracker }

func (w writer) Write(p []byte) (int, error) {
	// Non-TTY: mpb renders nothing and never flushes its write buffer (there
	// is no refresh tick), so log lines must go straight to out — otherwise
	// they would be swallowed.
	if !w.t.tty {
		return w.t.out.Write(p)
	}
	// TTY: route through mpb so the line is printed above the running bars.
	// mpb returns ErrDone after Shutdown; log lines racing Close() must not
	// turn into zerolog errors — they are progress-adjacent, not critical.
	if _, err := w.t.p.Write(p); err != nil {
		return len(p), nil
	}
	return len(p), nil
}

// logf writes a single milestone/notice line to the output on non-TTY. It is
// a no-op on a TTY, where bars already convey progress.
func (t *Tracker) logf(format string, args ...any) {
	if t.tty {
		return
	}
	_, _ = fmt.Fprintf(t.out, format+"\n", args...)
}

// barOptions are shared by the determinate (ffmpeg) and indeterminate
// (dovi_tool) bars: the file basename on the left, and removal from the
// container as soon as the bar completes/aborts (Finish drops it).
func barOptions(label string) []mpb.BarOption {
	return []mpb.BarOption{
		mpb.PrependDecorators(decor.Name(label, decor.WCSyncSpaceR)),
		mpb.BarRemoveOnComplete(),
	}
}

// Start registers a determinate bar (ffmpeg conversions; total = source size
// in bytes, driven by ffmpeg's total_size progress). On a TTY it renders as
// "name ███ 34% | 7.6/22 GiB | 187 MB/s | 0:42 | 1:19"; on a non-TTY a log
// line is emitted every 10% of progress.
func (t *Tracker) Start(key, label string, total int64) {
	opts := append(barOptions(label),
		mpb.AppendDecorators(
			decor.Percentage(decor.WCSyncSpace),
			decor.CountersKibiByte("% .1f/% .1f", decor.WCSyncSpace),
			decor.EwmaSpeed(decor.SizeB1024(0), "% .1f", 30, decor.WCSyncSpace),
			decor.Elapsed(decor.ET_STYLE_MMSS, decor.WCSyncSpace),
			decor.EwmaETA(decor.ET_STYLE_MMSS, 30, decor.WCSyncSpace),
		),
	)
	t.mu.Lock()
	t.bar[key] = &entry{bar: t.p.AddBar(total, opts...), label: label, total: total, nextPct: milestonePct}
	t.mu.Unlock()
}

// StartSpinner registers an indeterminate spinner for steps without
// measurable progress (dovi_tool RPU conversion). On a non-TTY only start and
// finish notices are logged (there is no measurable percentage).
func (t *Tracker) StartSpinner(key, label string) {
	t.mu.Lock()
	t.bar[key] = &entry{bar: t.p.AddSpinner(0, barOptions(label)...), label: label, total: 0}
	t.mu.Unlock()
	t.logf("working: %s", label)
}

// Set updates a bar's absolute progress. Unknown keys are ignored. On a
// non-TTY it also emits a log line each time a 10% milestone is crossed.
func (t *Tracker) Set(key string, current int64) {
	t.mu.Lock()
	e, ok := t.bar[key]
	if !ok {
		t.mu.Unlock()
		return
	}
	e.bar.SetCurrent(current)
	var label string
	var pct int
	logIt := false
	if !t.tty && e.total > 0 {
		if pct = int(current * 100 / e.total); pct >= e.nextPct {
			// Skip past any milestones jumped over in a single update.
			e.nextPct = (pct/milestonePct + 1) * milestonePct
			label, logIt = e.label, true
		}
	}
	t.mu.Unlock()
	if logIt {
		t.logf("progress: %s %d%%", label, pct)
	}
}

// Finish removes the bar. The caller logs the completion line right after,
// which is printed above the remaining bars via the Writer.
func (t *Tracker) Finish(key string) {
	t.mu.Lock()
	e, ok := t.bar[key]
	if !ok {
		t.mu.Unlock()
		return
	}
	// Abort works whether the bar completed or not (a failed ffmpeg run
	// leaves the bar short of its total); BarRemoveOnComplete drops it from
	// the rendered block at the next refresh.
	e.bar.Abort(true)
	delete(t.bar, key)
	t.mu.Unlock()
	t.logf("done: %s", e.label)
}
