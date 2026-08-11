package display

import (
	"bytes"
	"strings"
	"testing"
)

// TestNonTTYRendersNothing regress-tests the line-spam bug: when output is
// not a terminal (docker logs, redirected stderr, CI), mpb runs in no-render
// mode — bars emit nothing (no per-redraw lines, no ANSI cursor codes),
// while log lines written through Writer pass through.
func TestNonTTYRendersNothing(t *testing.T) {
	var buf bytes.Buffer
	tr := New(&buf)
	defer tr.Close()

	tr.Start("/x/movie.mkv", "movie.mkv", 100)
	for i := int64(1); i <= 10; i++ {
		tr.Set("/x/movie.mkv", i*10)
	}
	tr.Finish("/x/movie.mkv")

	// A normal log line written through Writer before Close must pass
	// through: non-TTY bar suppression must not swallow regular logs.
	if _, err := tr.Writer().Write([]byte("log line through writer\n")); err != nil {
		t.Fatalf("write before Close must not error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "log line through writer") {
		t.Fatalf("Writer output before Close must pass through, got %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("non-TTY output must not contain ANSI escapes, got %q", out)
	}
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, "|") || strings.Contains(line, "█") {
			t.Fatalf("non-TTY output must not contain bar glyphs, got line %q", line)
		}
	}
}

// TestNonTTYMilestoneLogs: on a non-TTY the tracker emits a progress log line
// each time a 10% milestone is crossed, plus start/finish notices for
// indeterminate spinners.
func TestNonTTYMilestoneLogs(t *testing.T) {
	var buf bytes.Buffer
	tr := New(&buf)
	defer tr.Close()

	tr.Start("/x/movie.mkv", "movie.mkv", 100)
	// Drive 0 -> 100 in steps; expect one line per 10% milestone.
	for i := int64(0); i <= 100; i++ {
		tr.Set("/x/movie.mkv", i)
	}
	tr.Finish("/x/movie.mkv")

	tr.StartSpinner("/x/other.mkv", "other.mkv")
	tr.Finish("/x/other.mkv")

	out := buf.String()
	for _, want := range []string{
		"progress: movie.mkv 10%",
		"progress: movie.mkv 50%",
		"progress: movie.mkv 100%",
		"done: movie.mkv",
		"working: other.mkv",
		"done: other.mkv",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing log line %q in:\n%s", want, out)
		}
	}
	// Exactly one line per milestone (no duplicates).
	if n := strings.Count(out, "progress: movie.mkv"); n != 10 {
		t.Errorf("expected 10 milestone lines, got %d:\n%s", n, out)
	}
}

// TestNonTTYMilestoneSkipsJumpedSteps: a big single update logs once for the
// reached percent (not one line per skipped milestone).
func TestNonTTYMilestoneSkipsJumpedSteps(t *testing.T) {
	var buf bytes.Buffer
	tr := New(&buf)
	defer tr.Close()

	tr.Start("/x/movie.mkv", "movie.mkv", 100)
	tr.Set("/x/movie.mkv", 45) // jumps 10,20,30,40 -> single 45% line
	out := buf.String()
	if !strings.Contains(out, "progress: movie.mkv 45%") {
		t.Errorf("expected a 45%% line, got:\n%s", out)
	}
	if strings.Count(out, "progress: movie.mkv") != 1 {
		t.Errorf("expected exactly 1 milestone line for a jump, got:\n%s", out)
	}
}

// TestWriterAfterClose pins the shutdown race: a log line racing Close must
// be acknowledged (so zerolog never errors) and must not panic — run with
// the race detector to cover the concurrent Close/Write paths.
func TestWriterAfterClose(t *testing.T) {
	tr := New(&bytes.Buffer{})
	tr.Start("/x/movie.mkv", "movie.mkv", 100)

	gate := make(chan struct{})
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.Close()
	}()
	go func() {
		<-gate
		_, err := tr.Writer().Write([]byte("late log\n"))
		errCh <- err
	}()
	close(gate)
	<-done
	if err := <-errCh; err != nil {
		t.Fatalf("write racing Close must not error: %v", err)
	}
	tr.Finish("/x/movie.mkv") // must not panic on an unknown key
}
