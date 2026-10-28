package rules

import (
	"testing"
	"time"
)

func TestIsGenericName(t *testing.T) {
	generic := []string{
		"IMG_20261021_113648", "PXL_20261021_113648577.MP", "DSC01234", "IMG-20261021-WA0003",
		"WhatsApp Image 2026-10-21 at 11.36.48", "Screenshot 2026-10-21 140322", "Screenshot_20261021-140322",
		"2026-06-26 13-43-59", "download (3)", "Untitled", "image (1)", "image-1792888971177", "document1",
		"a8f3c9d2e1b7a4c6", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "1019075630", "scan0001", "New Text Document", "track01", "Track 01",
	}
	for _, s := range generic {
		if !IsGenericName(s) {
			t.Errorf("%q should be generic", s)
		}
	}
	personal := []string{
		"AcmeLogo", "Project Plan — Design Document", "Model_Pack_18K_Tris_1019075630_texture_fbx",
		"instagram-someuser-2026-10-23-X9bN3nh3", "Tax return 2025", "Report_2", "company_logo_svg_ico", "Mum birthday",
	}
	for _, s := range personal {
		if IsGenericName(s) {
			t.Errorf("%q should be left alone", s)
		}
	}
}

func TestDateFromName(t *testing.T) {
	cases := map[string]string{
		"PXL_20261021_113648577.MP":             "2026-10-21 11:36:48",
		"2026-06-26 13-43-59":                   "2026-06-26 13:43:59",
		"WhatsApp Image 2026-10-21 at 11.36.48": "2026-10-21 11:36:48",
		"Screenshot 2026-10-21 140322":          "2026-10-21 14:03:22",
		"invoice 2025-03-04":                    "2025-03-04 00:00:00",
	}
	for in, want := range cases {
		got, ok := dateFromName(in)
		if !ok || got.Format("2006-01-02 15:04:05") != want {
			t.Errorf("%q → %v %v, want %s", in, got, ok, want)
		}
	}
	if _, ok := dateFromName("Model_Pack_18K_Tris_texture"); ok {
		t.Error("found a date where there is none")
	}
	if _, ok := dateFromName("image-1792888971177"); !ok {
		t.Error("millisecond timestamp not read")
	}
}

func TestSuggestName(t *testing.T) {
	d := time.Date(2026, 10, 21, 11, 36, 48, 0, time.Local)
	cases := []struct {
		in   Info
		want string
	}{
		{Info{Type: "image", Kind: "camera", Date: d, Ext: ".jpg"}, "Photo 2026-10-21 11.36.48.jpg"},
		{Info{Type: "image", Kind: "screenshot", Date: d, Ext: ".png"}, "Screenshot 2026-10-21 11.36.48.png"},
		{Info{Type: "video", Kind: "recording", Date: d, Ext: ".mp4"}, "Screen recording 2026-10-21 11.36.48.mp4"},
		{Info{Type: "document", Date: d, Ext: ".pdf", Title: "Annual Report: 2026"}, "Annual Report 2026.pdf"},
		{Info{Type: "other", Date: d, Ext: ".dat"}, "File 2026-10-21 11.36.48.dat"},
	}
	for _, c := range cases {
		if got := SuggestName(c.in); got != c.want {
			t.Errorf("SuggestName(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstWordAndKinds(t *testing.T) {
	for in, want := range map[string]string{"AcmeLogo": "Acme", "Acme_Voice_Nova": "Acme", "company logo": "company", "123": ""} {
		if got := firstWord(in); got != want {
			t.Errorf("firstWord(%q) = %q, want %q", in, got, want)
		}
	}
	if kindOf("PXL_20261021_113648577", "image") != "camera" || kindOf("Screenshot 2026-10-21", "image") != "screenshot" ||
		kindOf("2026-06-26 13-43-59", "video") != "recording" || kindOf("holiday", "image") != "" {
		t.Error("kind detection is off")
	}
}
