package cmd

import (
	"path/filepath"

	"github.com/Belphemur/dvstrip/internal/queue"

	"github.com/spf13/cobra"
)

var scanCmd = &cobra.Command{
	Use:   "scan <dir>",
	Short: "Recursively scan a folder and strip Dolby Vision from 4K HDR files",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}

		workers := resolveWorkers()
		q := queue.New(workers, handle)
		q.Start(cmd.Context())
		pkg.Info().Str("dir", dir).Int("workers", workers).Msg("scanning")

		if err := submitDir(q, dir); err != nil {
			return err
		}
		q.Wait()
		printStats()
		return nil
	},
}
