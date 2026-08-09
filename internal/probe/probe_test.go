package probe

import "testing"

func TestParseActionMatrix(t *testing.T) {
	cases := []struct {
		name    string
		is4k    bool
		hdr10   bool
		dv      bool
		dv5     bool
		hdr10p  bool
		proc    bool
		action  string
	}{
		{"p4k_hdr10_dv", true, true, true, false, false, false, "strip-dv"},
		{"p4k_hdr10_dv_p81", true, true, true, false, false, false, "strip-dv"},
		{"p4k_p5", true, true, true, true, false, false, "convert-p5 (dovi_tool p5->p8.1, then strip to HDR10)"},
		{"p4k_hdr10_plain", true, true, false, false, false, false, "none (already plain HDR10)"},
		{"p4k_sdr", true, false, false, false, false, false, "skip (not HDR10)"},
		{"fullhdr10plus", true, true, false, false, true, false, "none (already plain HDR10)"},
		{"already_marked", true, true, false, false, false, true, "skip (already processed by dvstrip)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := Parse(fixtures[c.name])
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if info.Is4K() != c.is4k {
				t.Errorf("Is4K() = %v, want %v", info.Is4K(), c.is4k)
			}
			if info.IsHDR10() != c.hdr10 {
				t.Errorf("IsHDR10() = %v, want %v", info.IsHDR10(), c.hdr10)
			}
			if (info.HasDV) != c.dv {
				t.Errorf("HasDV = %v, want %v", info.HasDV, c.dv)
			}
			if info.IsDVP5() != c.dv5 {
				t.Errorf("IsDVP5() = %v, want %v", info.IsDVP5(), c.dv5)
			}
			if info.HasHDR10Plus != c.hdr10p {
				t.Errorf("HasHDR10Plus = %v, want %v", info.HasHDR10Plus, c.hdr10p)
			}
			if info.Processed != c.proc {
				t.Errorf("Processed = %v, want %v", info.Processed, c.proc)
			}
			// compat 1/6 are the only HDR10-compatible DV cases in fixtures.
			wantCompat := c.dv && (c.name == "p4k_hdr10_dv" || c.name == "p4k_hdr10_dv_p81" || c.name == "fullhdr10plus")
			if info.DVHDR10Compatible() != wantCompat {
				t.Errorf("DVHDR10Compatible() = %v, want %v", info.DVHDR10Compatible(), wantCompat)
			}
			if info.Action() != c.action {
				t.Errorf("Action() = %q, want %q", info.Action(), c.action)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid json")
	}
	empty := []byte(`{"streams":[]}`)
	if _, err := Parse(empty); err == nil {
		t.Fatal("expected error for empty streams")
	}
}
