package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Belphemur/dvstrip/internal/convert"
	"github.com/Belphemur/dvstrip/internal/queue"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var watchCmd = &cobra.Command{
	Use:   "watch <dir>",
	Short: "Watch a folder and normalize new 4K HDR files as they appear",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()

		workers := resolveWorkers()
		q := queue.New(workers, handle)
		q.Start(ctx)

		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return err
		}
		defer func() { _ = watcher.Close() }()

		// fsnotify is not recursive: watch the root and every existing subdir.
		// Unreadable subtrees are skipped with a warning rather than aborting
		// the whole watch (mirrors CBZOptimizer's tolerant addRecursiveWatch).
		if err := addRecursiveWatch(watcher, dir); err != nil {
			return fmt.Errorf("failed to watch path %s: %w", dir, err)
		}
		// Sweep any tmp files left behind by a crashed run while we are here.
		// Tolerant of unreadable subtrees: skip (don't abort) on walk errors.
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				pkg.Warn().Err(err).Str("path", path).Msg("skipping path while sweeping temp files")
				return nil
			}
			if !d.IsDir() && convert.IsTemp(path) {
				pkg.Debug().Str("file", filepath.Base(path)).Msg("sweeping stale temp file")
				sweepTemp(path)
			}
			return nil
		}); err != nil {
			return err
		}

		pkg.Info().Str("dir", dir).Int("workers", workers).Msg("watching (Ctrl-C to stop)")

		// Optional initial pass over everything on disk. Queue dedup means it
		// is harmless if the watcher also fires for the same files.
		if viper.GetBool("full-scan") {
			pkg.Info().Str("dir", dir).Msg("full scan before watching")
			if err := submitDir(q, dir); err != nil {
				return err
			}
		}

		// Debounce bursts of Write events while a file is being copied, and
		// wait for the file size to settle before submitting.
		scheduler := newFileScheduler(viper.GetDuration("debounce"), queueSubmit(q))

		for {
			select {
			case <-ctx.Done():
				pkg.Info().Msg("shutting down, draining queue...")
				// Flush files still waiting out their debounce before
				// draining the queue, so scheduled work is never dropped.
				scheduler.Close()
				q.Wait()
				printStats()
				return nil
			case ev, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				pkg.Debug().Str("file", ev.Name).Str("op", ev.Op.String()).Msg("watcher event received")
				if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Rename) {
					if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
						// A directory was created or moved into the watched
						// tree (IN_MOVED_TO surfaces as a Rename event on the
						// live new path). Watch the whole subtree — not just
						// the top dir — so files added later inside nested
						// subdirs are still picked up, then schedule the
						// existing contents through the scheduler.
						pkg.Debug().Str("dir", ev.Name).Msg("directory arrival detected")
						if err := addRecursiveWatch(watcher, ev.Name); err != nil {
							// Without the watch, files inside would be
							// processed once but never monitored — skip
							// the scan to avoid that inconsistent state.
							pkg.Error().Err(err).Str("dir", ev.Name).Msg("failed to watch new directory")
							continue
						}
						pkg.Info().Str("dir", ev.Name).Msg("new directory detected, now watching")
						// The directory may already contain files (e.g. created
						// and populated in one shot, moved in, or hard-linked in
						// before the watcher was added). Schedule everything
						// inside it so files settle like any other event path.
						pkg.Debug().Str("dir", ev.Name).Msg("scanning existing files in new directory")
						scheduler.scheduleDir(ev.Name, func(err error) {
							pkg.Error().Err(err).Str("dir", ev.Name).Msg("failed to scan new directory")
						})
						continue
					}
					// Event on a file (not a directory) — fall through
					// to the scheduling logic below.
					pkg.Debug().Str("file", filepath.Base(ev.Name)).Msg("event on file")
				}
				if !shouldProcessWatchEvent(ev) || !isVideo(ev.Name) || isOwnOutput(ev.Name) {
					if !shouldProcessWatchEvent(ev) {
						pkg.Debug().Str("file", filepath.Base(ev.Name)).Str("op", ev.Op.String()).Msg("event filtered: no create/write/rename op")
					} else if !isVideo(ev.Name) {
						pkg.Debug().Str("file", filepath.Base(ev.Name)).Str("ext", filepath.Ext(ev.Name)).Msg("event filtered: not a video extension")
					} else {
						pkg.Debug().Str("file", filepath.Base(ev.Name)).Msg("event filtered: own output")
					}
					continue
				}
				pkg.Debug().Str("file", filepath.Base(ev.Name)).Str("op", ev.Op.String()).Msg("event scheduled")
				scheduler.schedule(ev.Name)
			case err, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
				pkg.Error().Err(err).Msg("watcher error")
			}
		}
	},
}

func init() {
	watchCmd.Flags().Bool("full-scan", false, "scan the whole directory once before watching for changes")
	if err := viper.BindPFlag("full-scan", watchCmd.Flags().Lookup("full-scan")); err != nil {
		panic(err)
	}
}

// addRecursiveWatch registers a watch on dir and every subdirectory beneath it.
// Directories that can't be read (e.g. permission errors) are logged and
// skipped instead of aborting the whole walk, so a single bad subtree doesn't
// prevent the rest of the tree from being watched. A non-permission failure
// adding a directory is returned so the caller can react (skip scheduling its
// contents), matching the tolerant error policy used at watch startup.
func addRecursiveWatch(watcher *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				pkg.Warn().Err(err).Str("dir", path).Msg("skipping unreadable directory")
				return nil
			}
			pkg.Warn().Err(err).Str("dir", path).Msg("skipping path while setting up watch")
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				pkg.Warn().Err(err).Str("dir", path).Msg("skipping unreadable directory")
				return nil
			}
			return fmt.Errorf("failed to watch directory %s: %w", path, err)
		}
		return nil
	})
}

// shouldProcessWatchEvent reports whether an fsnotify event should be routed
// toward the scheduler. Create, Write and Rename are all accepted: a file
// moved into the watched tree on the same filesystem surfaces as an IN_MOVED_TO
// Rename event (with the live path) and is never followed by a Create, while
// the stale old-path event (IN_MOVED_FROM, Rename with the gone path, or a file
// renamed away) is dropped later by the scheduler's existence check.
func shouldProcessWatchEvent(ev fsnotify.Event) bool {
	return ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write) || ev.Has(fsnotify.Rename)
}
