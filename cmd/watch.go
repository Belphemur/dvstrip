package cmd

import (
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
		// Sweep any tmp files left behind by a crashed run while we are here.
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				pkg.Debug().Str("dir", path).Msg("watching directory")
				return watcher.Add(path)
			}
			if convert.IsTemp(path) {
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
				if ev.Op&fsnotify.Create != 0 {
					if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
						// pick up new subdirectories
						pkg.Debug().Str("dir", ev.Name).Msg("create event is a directory")
						if err := watcher.Add(ev.Name); err != nil {
							// Without the watch, files inside would be
							// processed once but never monitored — skip
							// the scan to avoid that inconsistent state.
							pkg.Error().Err(err).Str("dir", ev.Name).Msg("failed to watch new directory")
							continue
						}
						pkg.Info().Str("dir", ev.Name).Msg("new directory detected, now watching")
						// The directory may already contain files (e.g. created
						// and populated in one shot, or hard-linked in before the
						// watcher was added). Schedule everything inside it so
						// files settle like any other event path.
						pkg.Debug().Str("dir", ev.Name).Msg("scanning existing files in new directory")
						scheduler.scheduleDir(ev.Name, func(err error) {
							pkg.Error().Err(err).Str("dir", ev.Name).Msg("failed to scan new directory")
						})
						continue
					}
					// Create event on a file (not a directory) — fall through
					// to the scheduling logic below.
					pkg.Debug().Str("file", filepath.Base(ev.Name)).Msg("create event on file")
				}
				// Rename-only events carry the old path, which no longer exists
				// (the destination fires its own Create) — nothing to schedule.
				if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 || !isVideo(ev.Name) || isOwnOutput(ev.Name) {
					if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
						pkg.Debug().Str("file", filepath.Base(ev.Name)).Str("op", ev.Op.String()).Msg("event filtered: no create/write op")
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
