package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// pkg is the package-wide logger handle. It is configured in Execute()/init
// from CLI flags (level + human/json output). Every command and worker writes
// through it, never through fmt.Print* or the stdlib log package.
var pkg zerolog.Logger

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "dvstrip",
	Short: "Find 4K HDR files carrying Dolby Vision and normalize them to HDR10 (lossless, no re-encode)",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		for _, bin := range []string{"ffprobe", "ffmpeg"} {
			if _, err := exec.LookPath(bin); err != nil {
				return fmt.Errorf("required binary %q not found in PATH", bin)
			}
		}
		return nil
	},
}

// Execute wires Ctrl-C / SIGTERM into the command context so the worker pool
// drains cleanly instead of being killed mid-remux.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
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
	pf.String("log-level", "info", "log level (trace|debug|info|warn|error)")
	pf.Bool("log-json", false, "emit JSON logs instead of human-readable")

	bound := []string{
		"workers", "extensions", "dry-run", "force", "replace", "suffix",
		"p5-mode", "hdr10plus", "debounce", "log-level", "log-json",
	}
	for _, n := range bound {
		if err := viper.BindPFlag(n, pf.Lookup(n)); err != nil {
			panic(err)
		}
	}

	rootCmd.AddCommand(scanCmd, watchCmd, checkCmd)
}

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

	// Configure the global logger once flags/env/config are loaded.
	out := os.Stderr
	if viper.GetBool("log-json") {
		pkg = zerolog.New(out).With().Timestamp().Logger()
	} else {
		pkg = zerolog.New(zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339}).
			With().Timestamp().Logger()
	}
	pkg = pkg.Level(logLevel(viper.GetString("log-level")))

	// Re-bind cobra's output so --help / errors go through the same sink.
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)

	if err := viper.ReadInConfig(); err == nil {
		pkg.Info().Str("config", viper.ConfigFileUsed()).Msg("using config file")
	}
}

func logLevel(s string) zerolog.Level {
	l, err := zerolog.ParseLevel(strings.ToLower(s))
	if err != nil {
		return zerolog.InfoLevel
	}
	return l
}
