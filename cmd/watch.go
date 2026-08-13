package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

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
				return watcher.Add(path)
			}
			if convert.IsTemp(path) {
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
		// wait for the file size to settle before submitting. This matters for
		// hard-linked files: the Create event may arrive while the shared inode
		// is still growing in another directory, so probing immediately would
		// see an incomplete file. We reschedule until the size stops changing
		// for the configured debounce interval.
		type pending struct {
			timer *time.Timer
			size  int64
		}
		var mu sync.Mutex
		timers := map[string]*pending{}
		delay := viper.GetDuration("debounce")
		schedule := func(path string) {
			mu.Lock()
			defer mu.Unlock()

			curSize := int64(-1)
			if st, err := os.Stat(path); err == nil {
				curSize = st.Size()
			}

			p, ok := timers[path]
			if ok {
				p.timer.Stop()
				p.size = curSize
			} else {
				p = &pending{size: curSize}
				timers[path] = p
			}

			p.timer = time.AfterFunc(delay, func() {
				mu.Lock()
				nowSize := int64(-1)
				if st, err := os.Stat(path); err == nil {
					nowSize = st.Size()
				}
				if nowSize != p.size {
					// Still growing (or disappeared and reappeared). Reset the
					// timer with the new baseline instead of submitting.
					p.size = nowSize
					p.timer.Reset(delay)
					mu.Unlock()
					return
				}
				delete(timers, path)
				mu.Unlock()
				q.Submit(queue.Job{Path: path})
			})
		}

		for {
			select {
			case <-ctx.Done():
				pkg.Info().Msg("shutting down, draining queue...")
				q.Wait()
				printStats()
				return nil
			case ev, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				if ev.Op&fsnotify.Create != 0 {
					if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
						_ = watcher.Add(ev.Name) // pick up new subdirectories
						// The directory may already contain files (e.g. created
						// and populated in one shot, or hard-linked in before the
						// watcher was added). Submit everything inside it now.
						_ = submitDir(q, ev.Name)
						continue
					}
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 || !isVideo(ev.Name) || isOwnOutput(ev.Name) {
					continue
				}
				schedule(ev.Name)
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
