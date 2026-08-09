package cmd

import (
	"github.com/Belphemur/dvstrip/internal/probe"

	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check <file>",
	Short: "Probe a single file and print its HDR / Dolby Vision status and intended action",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := probe.Probe(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		e := pkg.Info().
			Str("file", info.Path).
			Int("width", info.Width).
			Int("height", info.Height).
			Bool("is_4k", info.Is4K()).
			Str("codec", info.Codec).
			Str("pix_fmt", info.PixFmt).
			Str("color_transfer", info.ColorTransfer).
			Bool("is_hdr10", info.IsHDR10()).
			Str("color_primaries", info.ColorPrimaries).
			Bool("has_dv", info.HasDV).
			Bool("has_hdr10+", info.HasHDR10Plus).
			Bool("processed", info.Processed).
			Str("action", info.Action())
		if info.HasDV {
			e = e.Int("dv_profile", info.DVProfile).
				Int("dv_compat", info.DVCompat).
				Bool("dv_hdr10_compatible", info.DVHDR10Compatible())
		}
		if info.Processed {
			e = e.Str("processed_note", info.ProcessedNote)
		}
		e.Send()
		return nil
	},
}
