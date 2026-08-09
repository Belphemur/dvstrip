package probe

import "encoding/json"

// ffprobeJSON builds a minimal ffprobe JSON document with a single video
// stream and optional side data + format tags, for parse tests only.
// dvProfile < 0 means "no DOVI side data".
func ffprobeJSON(width, height int, transfer, primaries string, dvProfile int, dvCompat int, hdr10plus bool, formatTags map[string]string) []byte {
	var sd []map[string]any
	if dvProfile >= 0 {
		sd = append(sd, map[string]any{
			"side_data_type":                "DOVI configuration record",
			"dv_profile":                    dvProfile,
			"dv_level":                      0,
			"dv_bl_signal_compatibility_id": dvCompat,
			"rpu_present_flag":              1,
			"el_present_flag":               1,
			"bl_present_flag":               1,
		})
	}
	if hdr10plus {
		sd = append(sd, map[string]any{"side_data_type": "HDR10+ Metadata"})
	}

	doc := map[string]any{
		"streams": []map[string]any{{
			"codec_name":      "hevc",
			"codec_type":      "video",
			"width":           width,
			"height":          height,
			"pix_fmt":         "yuv420p10le",
			"color_transfer":  transfer,
			"color_primaries": primaries,
			"side_data_list":  sd,
		}},
	}
	if formatTags != nil {
		doc["format"] = map[string]any{"tags": formatTags}
	}
	b, _ := json.Marshal(doc)
	return b
}

// Cases map to the cmd decision matrix.
var fixtures = map[string][]byte{
	"p4k_hdr10_dv":     ffprobeJSON(3840, 2160, "smpte2084", "bt2020", 7, 6, false, nil),
	"p4k_hdr10_dv_p81": ffprobeJSON(3840, 2160, "smpte2084", "bt2020", 8, 1, false, nil),
	"p4k_p5":           ffprobeJSON(3840, 2160, "smpte2084", "bt2020", 5, 0, false, nil),
	"p4k_hdr10_plain":  ffprobeJSON(3840, 2160, "smpte2084", "bt2020", -1, 0, false, nil),
	"p4k_sdr":          ffprobeJSON(3840, 2160, "bt709", "bt709", -1, 0, false, nil),
	"fullhdr10plus":    ffprobeJSON(3840, 2160, "smpte2084", "bt2020", -1, 0, true, nil),
	"already_marked": ffprobeJSON(3840, 2160, "smpte2084", "bt2020", -1, 0, false,
		map[string]string{"dvstrip": "1", "comment": "dvstrip: hdr10 -> normalized"}),
}
