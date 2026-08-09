// Package probe wraps ffprobe to extract HDR / Dolby Vision metadata from a
// video file. It only shells out to ffprobe; all parsing is done in-process
// so the decision logic is fully unit-testable from recorded JSON fixtures.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// MarkerKey is the container-level tag we stamp on processed files so that
// subsequent runs can skip them. Read back via ffprobe format_tags; matched
// case-insensitively because MP4 lowercases tag keys while MKV preserves them.
const MarkerKey = "dvstrip"

type sideData struct {
	SideDataType string `json:"side_data_type"`
	DVProfile    int    `json:"dv_profile"`
	DVCompatID   int    `json:"dv_bl_signal_compatibility_id"`
}

type stream struct {
	CodecName      string     `json:"codec_name"`
	Width          int        `json:"width"`
	Height         int        `json:"height"`
	PixFmt         string     `json:"pix_fmt"`
	ColorTransfer  string     `json:"color_transfer"`
	ColorPrimaries string     `json:"color_primaries"`
	SideDataList   []sideData `json:"side_data_list"`
}

type ffprobeOutput struct {
	Streams []stream `json:"streams"`
	Format  struct {
		Tags map[string]string `json:"tags"`
	} `json:"format"`
}

// Info is the HDR-relevant metadata of the first video stream of a file.
type Info struct {
	Path           string
	Codec          string
	Width, Height  int
	PixFmt         string
	ColorTransfer  string
	ColorPrimaries string
	HasDV          bool
	DVProfile      int
	DVCompat       int
	HasHDR10Plus   bool
	Processed      bool   // dvstrip marker tag present
	ProcessedNote  string // marker/comment tag value
}

// Is4K reports whether the stream is at least UHD width.
func (i Info) Is4K() bool { return i.Width >= 3840 }

// IsHDR10 reports whether the stream carries HDR10 signaling (PQ + BT.2020).
func (i Info) IsHDR10() bool {
	return i.ColorTransfer == "smpte2084" && i.ColorPrimaries == "bt2020"
}

// IsDVP5 reports whether the stream carries Dolby Vision profile 5.
func (i Info) IsDVP5() bool { return i.HasDV && i.DVProfile == 5 }

// DVHDR10Compatible reports whether the DV base layer is HDR10-compatible.
// Compat IDs 1 and 6 (profiles 7.6, 8.1, ...) are safe to strip losslessly.
func (i Info) DVHDR10Compatible() bool { return i.DVCompat == 1 || i.DVCompat == 6 }

// Action strings returned by Info.Action. Exported so callers (and tests)
// can compare against them instead of duplicating literals.
const (
	ActionSkipMarked   = "skip (already processed by dvstrip)"
	ActionSkipNot4K    = "skip (not 4K)"
	ActionSkipNotHDR10 = "skip (not HDR10)"
	ActionNone         = "none (already plain HDR10)"
	ActionStripDV      = "strip-dv"
	ActionConvertP5    = "convert-p5 (dovi_tool p5->p8.1, then strip to HDR10)"
	ActionManual       = "manual (unsupported DV profile/compat)"
)

// Action decides what dvstrip should do with the file.
func (i Info) Action() string {
	switch {
	case i.Processed:
		return ActionSkipMarked
	case !i.Is4K():
		return ActionSkipNot4K
	case i.IsDVP5():
		return ActionConvertP5
	case !i.IsHDR10():
		return ActionSkipNotHDR10
	case !i.HasDV:
		return ActionNone
	case i.DVHDR10Compatible():
		return ActionStripDV
	default:
		return ActionManual
	}
}

// Probe runs ffprobe on path and extracts HDR/DV metadata.
func Probe(ctx context.Context, path string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,pix_fmt,color_transfer,color_primaries:stream_side_data:format_tags",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe: %w", err)
	}
	info, err := Parse(out)
	if err != nil {
		return Info{}, err
	}
	info.Path = path
	return info, nil
}

// Parse decodes raw ffprobe JSON (for the first video stream + format tags)
// into an Info. Separated from Probe so the detection logic is unit-testable
// without an ffprobe binary.
func Parse(raw []byte) (Info, error) {
	var p ffprobeOutput
	if err := json.Unmarshal(raw, &p); err != nil {
		return Info{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	if len(p.Streams) == 0 {
		return Info{}, fmt.Errorf("no video stream")
	}

	s := p.Streams[0]
	info := Info{
		Codec:          s.CodecName,
		Width:          s.Width,
		Height:         s.Height,
		PixFmt:         s.PixFmt,
		ColorTransfer:  s.ColorTransfer,
		ColorPrimaries: s.ColorPrimaries,
	}
	for _, sd := range s.SideDataList {
		t := strings.ToLower(sd.SideDataType)
		switch {
		case t == "dovi configuration record":
			info.HasDV = true
			info.DVProfile = sd.DVProfile
			info.DVCompat = sd.DVCompatID
		case strings.Contains(t, "hdr10+"):
			info.HasHDR10Plus = true
		}
	}
	for k, v := range p.Format.Tags {
		lk, lv := strings.ToLower(k), strings.ToLower(v)
		if lk == MarkerKey || (lk == "comment" && strings.Contains(lv, MarkerKey)) {
			info.Processed = true
			info.ProcessedNote = v
		}
	}
	return info, nil
}
