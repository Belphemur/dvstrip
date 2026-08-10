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

		// Debounce bursts of Write events while a file is being copied.
		var mu sync.Mutex
		timers := map[string]*time.Timer{}
		delay := viper.GetDuration("debounce")
		schedule := func(path string) {
			mu.Lock()
			defer mu.Unlock()
			if t, ok := timers[path]; ok {
				t.Stop()
			}
			timers[path] = time.AfterFunc(delay, func() {
				mu.Lock()
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
						continue
					}
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 || !isVideo(ev.Name) || isOwnOutput(ev.Name) {
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
