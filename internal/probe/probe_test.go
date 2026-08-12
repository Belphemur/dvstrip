package probe

import "testing"

func TestParseActionMatrix(t *testing.T) {
	cases := []struct {
		name    string
		is4k    bool
		hdr10   bool
		dv      bool
		dv5     bool
		compat  bool
		hdr10p  bool
		proc    bool
		av1     bool
		stripOk bool
		action  string
	}{
		{"p4k_hdr10_dv", true, true, true, false, true, false, false, false, true, ActionStripDV},
		{"p4k_hdr10_dv_p81", true, true, true, false, true, false, false, false, true, ActionStripDV},
		{"p4k_p5", true, true, true, true, false, false, false, false, true, ActionConvertP5},
		{"p4k_hdr10_plain", true, true, false, false, false, false, false, false, true, ActionNone},
		{"p4k_sdr", true, false, false, false, false, false, false, false, true, ActionSkipNotHDR10},
		{"fullhdr10plus", true, true, false, false, false, true, false, false, true, ActionNone},
		{"av1_hdr10_dv", true, true, true, false, true, false, false, true, true, ActionStripDV},
		{"av1_hdr10_dv_p7", true, true, true, false, true, true, false, true, true, ActionStripDV},
		{"av1_hdr10_plain", true, true, false, false, false, false, false, true, true, ActionNone},
		{"already_marked", true, true, false, false, false, false, true, false, true, ActionSkipMarked},
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
			if info.DVHDR10Compatible() != c.compat {
				t.Errorf("DVHDR10Compatible() = %v, want %v", info.DVHDR10Compatible(), c.compat)
			}
			if info.IsAV1() != c.av1 {
				t.Errorf("IsAV1() = %v, want %v", info.IsAV1(), c.av1)
			}
			if info.StripSupported() != c.stripOk {
				t.Errorf("StripSupported() = %v, want %v", info.StripSupported(), c.stripOk)
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
