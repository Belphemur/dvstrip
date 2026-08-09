// Package display renders one progress bar per in-flight conversion at the
// bottom of the terminal. All terminal output — bars and log lines alike —
// goes through the Tracker's mutex so bars and zerolog lines never interleave
// mid-line.
package display

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

const redrawInterval = 120 * time.Millisecond

type entry struct {
	bar *progressbar.ProgressBar
}

// Tracker owns the terminal: workers register/unregister bars by key (file
// path), and a render loop redraws the bar block using ANSI cursor controls.
type Tracker struct {
	mu    sync.Mutex
	out   *os.File
	bars  map[string]*entry
	order []string // stable top-to-bottom line order
	drawn int      // bar lines currently on screen
	dirty bool
	done  chan struct{}
	wg    sync.WaitGroup
}

// New starts a Tracker rendering to out (normally os.Stderr).
func New(out *os.File) *Tracker {
	t := &Tracker{
		out:  out,
		bars: make(map[string]*entry),
		done: make(chan struct{}),
	}
	t.wg.Add(1)
	go t.loop()
	return t
}

func (t *Tracker) loop() {
	defer t.wg.Done()
	ticker := time.NewTicker(redrawInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.mu.Lock()
			if t.dirty {
				t.renderLocked()
				t.dirty = false
			}
			t.mu.Unlock()
		}
	}
}

// Close stops the render loop and clears any bars still on screen.
func (t *Tracker) Close() {
	close(t.done)
	t.wg.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearLocked()
}

// Writer returns the writer log output must go through: each write clears the
// bar block first, so a log line never lands in the middle of a bar.
func (t *Tracker) Writer() io.Writer { return writer{t} }

type writer struct{ t *Tracker }

func (w writer) Write(p []byte) (int, error) {
	w.t.mu.Lock()
	defer w.t.mu.Unlock()
	w.t.clearLocked()
	n, err := w.t.out.Write(p)
	w.t.dirty = true
	return n, err
}

// Start registers a determinate bar (ffmpeg conversions; total = source size
// in bytes, driven by ffmpeg's total_size progress).
func (t *Tracker) Start(key, label string, total int64) {
	bar := progressbar.NewOptions64(total,
		progressbar.OptionSetDescription(label),
		progressbar.OptionSetWriter(io.Discard), // rendered via String()
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionThrottle(redrawInterval),
	)
	t.mu.Lock()
	t.bars[key] = &entry{bar: bar}
	t.order = append(t.order, key)
	t.dirty = true
	t.mu.Unlock()
}

// StartSpinner registers an indeterminate spinner for steps without
// measurable progress (dovi_tool RPU conversion).
func (t *Tracker) StartSpinner(key, label string) {
	bar := progressbar.NewOptions64(-1,
		progressbar.OptionSetDescription(label),
		progressbar.OptionSetWriter(io.Discard),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetSpinnerChangeInterval(redrawInterval),
		progressbar.OptionSetWidth(25),
	)
	t.mu.Lock()
	t.bars[key] = &entry{bar: bar}
	t.order = append(t.order, key)
	t.dirty = true
	t.mu.Unlock()
}

// Set updates a bar's absolute progress. Unknown keys are ignored.
func (t *Tracker) Set(key string, current int64) {
	t.mu.Lock()
	if e, ok := t.bars[key]; ok {
		_ = e.bar.Set64(current)
		t.dirty = true
	}
	t.mu.Unlock()
}

// Finish removes the bar. The caller logs the completion line right after,
// which clears and redraws the remaining bars via the Writer.
func (t *Tracker) Finish(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.bars[key]
	if !ok {
		return
	}
	_ = e.bar.Finish()
	delete(t.bars, key)
	for i, k := range t.order {
		if k == key {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	t.dirty = true
}

// renderLocked redraws the whole bar block below any previously drawn lines.
func (t *Tracker) renderLocked() {
	t.clearLocked()
	if len(t.order) == 0 {
		return
	}
	var b strings.Builder
	for _, k := range t.order {
		b.WriteString("\x1b[2K") // clear line
		b.WriteString(t.bars[k].bar.String())
		b.WriteByte('\n')
	}
	_, _ = fmt.Fprint(t.out, b.String())
	t.drawn = len(t.order)
}

// clearLocked moves the cursor above the bar block and clears to screen end.
func (t *Tracker) clearLocked() {
	if t.drawn == 0 {
		return
	}
	_, _ = fmt.Fprintf(t.out, "\x1b[%dA\x1b[0J", t.drawn)
	t.drawn = 0
}
