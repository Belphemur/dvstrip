// Package cmd wires the dvstrip CLI: cobra commands, viper configuration,
// the zerolog instance and the progress tracker shared by all commands and
// worker goroutines.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Belphemur/dvstrip/internal/convert"
	"github.com/Belphemur/dvstrip/internal/display"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// pkg is the package-wide logger; rebuilt from flags/config in setupLogging.
// tracker renders per-file progress bars; nil when progress is disabled.
// spaceGuard keeps concurrent conversions from collectively exceeding the
// free disk space. All three are written through by every command and
// worker, never via fmt.Print*.
var (
	pkg        = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	tracker    *display.Tracker
	spaceGuard = convert.NewSpaceGuard()
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "dvstrip",
	Short: "Find 4K HDR files carrying Dolby Vision and normalize them to HDR10 (lossless, no re-encode)",
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, bin := range []string{"ffprobe", "ffmpeg"} {
			if _, err := exec.LookPath(bin); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", bin)
			}
		}
		setupLogging(cmd.Name())
		return nil
	},
}

// Execute wires Ctrl-C / SIGTERM into the command context so the worker pool
// drains cleanly instead of being killed mid-remux.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := rootCmd.ExecuteContext(ctx)
	stop()
	if tracker != nil {
		tracker.Close()
	}
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	pf := rootCmd.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "", "config file (default: ./dvstrip.yaml)")
	pf.IntP("workers", "w", 0, "concurrent ffmpeg workers (0 = auto)")
	pf.StringSliceP("extensions", "e", []string{".mkv", ".mp4", ".ts", ".m2ts"}, "video extensions to consider")
	pf.Bool("dry-run", false, "detect only, do not run ffmpeg")
	pf.Bool("force", false, "reprocess files even if they carry the dvstrip marker tag")
	pf.Bool("replace", false, "overwrite the original file after a verified conversion")
	pf.String("suffix", ".hdr10", "suffix inserted before the extension of output files (ignored with --replace)")
	pf.String("p5-mode", "convert", "how to handle DV profile 5: convert|skip")
	pf.Bool("hdr10plus", false, "preserve HDR10+ metadata if present, fall back to HDR10 otherwise")
	pf.Duration("debounce", 5*time.Second, "settle time before processing a changed file (watch mode)")
	pf.Bool("no-progress", false, "disable per-file progress bars (shown by default; forced off with --log-json)")
	pf.String("log-level", "info", "log level (trace|debug|info|warn|error)")
	pf.Bool("log-json", false, "emit JSON logs instead of human-readable")

	bound := []string{
		"workers", "extensions", "dry-run", "force", "replace", "suffix",
		"p5-mode", "hdr10plus", "debounce", "no-progress", "log-level", "log-json",
	}
	for _, n := range bound {
		if err := viper.BindPFlag(n, pf.Lookup(n)); err != nil {
			panic(err)
		}
	}

	rootCmd.AddCommand(scanCmd, watchCmd, checkCmd)
}

// initConfig only loads configuration; logging is built in PersistentPreRunE
// so config-file values (log-json, log-level, no-progress) are honored.
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("dvstrip")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.config/dvstrip")
	}
	viper.SetEnvPrefix("DVSTRIP")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	_ = viper.ReadInConfig()
}

// progressEnabled: bars are on by default; --no-progress opts out, and
// --log-json forces them off so the JSON stream stays machine-parseable.
func progressEnabled() bool {
	return !viper.GetBool("no-progress") && !viper.GetBool("log-json")
}

// setupLogging builds the shared logger. For commands that convert (scan,
// watch) on an interactive terminal, log lines are routed through the
// progress tracker so they never garble the bars.
func setupLogging(cmdName string) {
	var out io.Writer = os.Stderr
	switch cmdName {
	case "scan", "watch":
		if progressEnabled() {
			tracker = display.New(os.Stderr)
			out = tracker.Writer()
		}
	}

	if viper.GetBool("log-json") {
		pkg = zerolog.New(out).With().Timestamp().Logger()
	} else {
		pkg = zerolog.New(zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339}).
			With().Timestamp().Logger()
	}
	pkg = pkg.Level(logLevel(viper.GetString("log-level")))

	if cfg := viper.ConfigFileUsed(); cfg != "" {
		pkg.Info().Str("config", cfg).Msg("using config file")
	}
}

func logLevel(s string) zerolog.Level {
	l, err := zerolog.ParseLevel(strings.ToLower(s))
	if err != nil {
		return zerolog.InfoLevel
	}
	return l
}
