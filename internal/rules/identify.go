package rules

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/dhowden/tag"
	"github.com/gabriel-vasile/mimetype"
	"github.com/rwcarlsen/goexif/exif"
)

// Info is what the organizer works out about a file before sorting it.
type Info struct {
	Type    string    // image, video, document, … or "other"
	Kind    string    // screenshot, camera, recording, or ""
	Date    time.Time // when it was taken/made: EXIF, a date in the name, or mtime
	Title   string    // a title found inside the file (song, PDF, Office), or ""
	Generic bool      // the name was made up by a device or app, not by a person
	Ext     string    // lower-case extension with dot; filled in from content if missing
}

// Identify inspects a file. It reads only small headers, never whole files.
func Identify(path, name string, mtime int64) Info {
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	in := Info{Type: TypeOf(ext), Ext: ext, Date: time.Unix(0, mtime)}
	if in.Type == "other" {
		// Unknown or missing extension: look at the content.
		if m, err := mimetype.DetectFile(path); err == nil {
			if t := TypeOf(m.Extension()); t != "other" {
				in.Type = t
				if ext == "" || in.Ext == ".bin" {
					in.Ext = m.Extension()
				}
			}
		}
	}
	in.Generic = IsGenericName(stem)
	in.Kind = kindOf(stem, in.Type)
	exifCamera := false
	if d, ok := dateFromName(stem); ok {
		in.Date = d
	} else if in.Type == "image" && (ext == ".jpg" || ext == ".jpeg" || ext == ".tif" || ext == ".tiff") {
		if d, camera, ok := exifInfo(path); ok {
			in.Date = d
			exifCamera = camera
		}
	}
	if in.Kind == "" && exifCamera {
		in.Kind = "camera"
	}
	if in.Generic {
		in.Title = titleOf(path, in.Ext, in.Type)
	}
	return in
}

var (
	screenshotRe = regexp.MustCompile(`(?i)^(screenshot|screen shot|screen_shot|screencap|screen capture|capture d.écran|snip|scr[_-]?\d)`)
	recordingRe  = regexp.MustCompile(`(?i)^(screen recording|screenrecording|screen_recording|recording|\d{4}-\d{2}-\d{2}[ _]\d{2}-\d{2}-\d{2}$)`)
	cameraRe     = regexp.MustCompile(`(?i)^((img|vid|pxl|mvimg|dsc[nf]?|dcim|gopr|g[hx]\d{2}|dji|imag|sam|wp|p\d{3})[_-]?\d{3,}|(img|vid|aud)-\d{8}-wa\d+|whatsapp (image|video) |signal-\d{4}-|photo_\d{4}-)`)
)

func kindOf(stem, typ string) string {
	switch {
	case typ == "image" && screenshotRe.MatchString(stem):
		return "screenshot"
	case typ == "video" && (recordingRe.MatchString(stem) || screenshotRe.MatchString(stem)):
		return "recording"
	case (typ == "image" || typ == "video") && cameraRe.MatchString(stem):
		return "camera"
	}
	return ""
}

var (
	copySuffixRe = regexp.MustCompile(`(?i)( \(\d+\)| - copy( \(\d+\))?|[_ -]copy\d*|[_-]\d{1,2})$`)
	genericWords = regexp.MustCompile(`(?i)^(untitled|unnamed|unknown|image|images|img|picture|pic|photo|download|downloads|document|doc|file|new document|new text document|new microsoft \w+ document|new bitmap image|scan|scanned|export|exported|output|attachment|audio|video|recording|voice|voice memo|clip|movie|track|audio track|default|blob|media|temp|tmp)([ _\-]?\(?\d+\)?)?$`)
	hexRe        = regexp.MustCompile(`(?i)^[0-9a-f]{12,}$`)
	uuidRe       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	digitsRe     = regexp.MustCompile(`^[\d _.\-]{6,}$`)
	wordRe       = regexp.MustCompile(`[a-zA-Z]{4,}`)
)

// IsGenericName reports whether a filename (without extension) was made up
// by a camera, phone, browser or app rather than chosen by a person:
// IMG_1234, PXL_20261021_113648, Screenshot 2026-10-21, download (3),
// Untitled, a hash or UUID, or a bare timestamp.
func IsGenericName(stem string) bool {
	s := strings.TrimSpace(stem)
	for {
		t := copySuffixRe.ReplaceAllString(s, "")
		if t == s {
			break
		}
		s = strings.TrimSpace(t)
	}
	switch {
	case s == "":
		return true
	case genericWords.MatchString(s), cameraRe.MatchString(s), screenshotRe.MatchString(s),
		recordingRe.MatchString(s), hexRe.MatchString(s), uuidRe.MatchString(s), digitsRe.MatchString(s):
		return true
	}
	// Long tokens that are mostly digits and contain no real words, like
	// "a8f3c9d2e1b7" or "1019075630x9": generated, not typed.
	compact := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
	if len(compact) >= 12 && !strings.ContainsRune(s, ' ') && !wordRe.MatchString(s) {
		return true
	}
	return false
}

var nameDateRes = []*regexp.Regexp{
	// 20261021_113648, 20261021-140322
	regexp.MustCompile(`(20\d{2})(\d{2})(\d{2})[_-]?(\d{2})(\d{2})(\d{2})`),
	// 2026-10-21 13-43-59, 2026-10-21_13.43.59, 2026-10-21 at 11.36.48, 2026-10-21 140322
	regexp.MustCompile(`(20\d{2})-(\d{2})-(\d{2})(?:[ _T]|\s+at\s+)(\d{1,2})[-.:h]?(\d{2})[-.:m]?(\d{2})`),
	// date only: 2026-10-21, 20261021
	regexp.MustCompile(`(20\d{2})-?(\d{2})-?(\d{2})()()()`),
}

// epochMsRe finds a 13-digit millisecond timestamp, as browsers and chat
// apps put in names like image-1792888971177.png.
var epochMsRe = regexp.MustCompile(`(?:^|\D)(1\d{12})(?:\D|$)`)

// dateFromName finds a plausible date (and time) written in a filename.
func dateFromName(stem string) (time.Time, bool) {
	if t, ok := dateFromPattern(stem); ok {
		return t, true
	}
	if m := epochMsRe.FindStringSubmatch(stem); m != nil {
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		t := time.UnixMilli(ms)
		if t.Year() >= 2001 && t.Before(time.Now().AddDate(1, 0, 0)) {
			return t, true
		}
	}
	return time.Time{}, false
}

func dateFromPattern(stem string) (time.Time, bool) {
	for _, re := range nameDateRes {
		m := re.FindStringSubmatch(stem)
		if m == nil {
			continue
		}
		n := make([]int, 7)
		for i := 1; i <= 6; i++ {
			n[i], _ = strconv.Atoi(m[i])
		}
		if n[1] < 2000 || n[1] > time.Now().Year()+1 || n[2] < 1 || n[2] > 12 || n[3] < 1 || n[3] > 31 ||
			n[4] > 23 || n[5] > 59 || n[6] > 59 {
			continue
		}
		return time.Date(n[1], time.Month(n[2]), n[3], n[4], n[5], n[6], 0, time.Local), true
	}
	return time.Time{}, false
}

// exifInfo reads the date a photo was taken, and whether a camera model is
// recorded.
func exifInfo(path string) (time.Time, bool, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false, false
	}
	defer f.Close()
	x, err := exif.Decode(io.LimitReader(f, 1<<20))
	if err != nil {
		return time.Time{}, false, false
	}
	_, merr := x.Get(exif.Model)
	t, err := x.DateTime()
	if err != nil || t.Year() < 1990 {
		return time.Time{}, merr == nil, false
	}
	return t, merr == nil, true
}

// titleOf finds a human title inside a file: song tags, PDF or Office
// document properties.
func titleOf(path, ext, typ string) string {
	var t string
	switch {
	case typ == "audio":
		t = audioTitle(path)
	case ext == ".pdf":
		t = pdfTitle(path)
	case ext == ".docx" || ext == ".xlsx" || ext == ".pptx":
		t = officeTitle(path)
	}
	return cleanTitle(t)
}

func audioTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil || strings.TrimSpace(m.Title()) == "" {
		return ""
	}
	if a := strings.TrimSpace(m.Artist()); a != "" {
		return a + " - " + strings.TrimSpace(m.Title())
	}
	return m.Title()
}

var (
	pdfTitleRe    = regexp.MustCompile(`/Title\s*\(((?:\\.|[^\\)])*)\)`)
	pdfHexTitleRe = regexp.MustCompile(`/Title\s*<([0-9A-Fa-f\s]+)>`)
	xmpTitleRe    = regexp.MustCompile(`(?s)<dc:title>.*?<rdf:li[^>]*>(.*?)</rdf:li>`)
)

// pdfTitle looks for the document title in the PDF's info dictionary or XMP
// metadata, which usually sit near the start or end of the file.
func pdfTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const chunk = 256 << 10
	var buf []byte
	head := make([]byte, min(chunk, fi.Size()))
	n, _ := f.ReadAt(head, 0)
	buf = append(buf, head[:n]...)
	if fi.Size() > chunk {
		tail := make([]byte, min(chunk, fi.Size()-chunk))
		n, _ := f.ReadAt(tail, fi.Size()-int64(len(tail)))
		buf = append(buf, tail[:n]...)
	}
	if m := xmpTitleRe.FindSubmatch(buf); m != nil {
		return string(m[1])
	}
	if m := pdfHexTitleRe.FindSubmatch(buf); m != nil {
		b, err := hex.DecodeString(strings.Join(strings.Fields(string(m[1])), ""))
		if err == nil {
			return decodePDFString(b)
		}
	}
	if m := pdfTitleRe.FindSubmatch(buf); m != nil {
		return decodePDFString(unescapePDF(m[1]))
	}
	return ""
}

func unescapePDF(b []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 == len(b) {
			out.WriteByte(b[i])
			continue
		}
		i++
		switch b[i] {
		case 'n', 'r', 't':
			out.WriteByte(' ')
		default:
			if b[i] >= '0' && b[i] <= '7' {
				j := i
				for j < len(b) && j < i+3 && b[j] >= '0' && b[j] <= '7' {
					j++
				}
				v, _ := strconv.ParseUint(string(b[i:j]), 8, 8)
				out.WriteByte(byte(v))
				i = j - 1
			} else {
				out.WriteByte(b[i])
			}
		}
	}
	return out.Bytes()
}

// decodePDFString handles UTF-16BE (with BOM) and plain 8-bit strings.
func decodePDFString(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	}
	return string(b)
}

var coreTitleRe = regexp.MustCompile(`(?s)<dc:title>(.*?)</dc:title>`)

func officeTitle(path string) string {
	z, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "docProps/core.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
		rc.Close()
		if m := coreTitleRe.FindSubmatch(b); m != nil {
			return xmlUnescape(string(m[1]))
		}
	}
	return ""
}

func xmlUnescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'").Replace(s)
}

var badTitleRe = regexp.MustCompile(`(?i)^(untitled|title|document\d*|presentation\d*|book\d*|slide \d+|powerpoint presentation|microsoft word - .*\.docx?|.*\.(docx?|pdf|indd|tmp))$`)

// cleanTitle keeps titles that look like a person wrote them.
func cleanTitle(t string) string {
	t = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t)), " ")
	t = strings.TrimPrefix(t, "Microsoft Word - ")
	if len([]rune(t)) < 3 || badTitleRe.MatchString(t) || IsGenericName(t) || !wordRe.MatchString(t) {
		return ""
	}
	return t
}

var typeLabel = map[string]string{
	"image": "Image", "video": "Video", "audio": "Audio", "document": "Document", "spreadsheet": "Spreadsheet",
	"presentation": "Presentation", "archive": "Archive", "installer": "Installer", "code": "Code",
	"font": "Font", "3d": "3D model", "ebook": "eBook",
}

// SuggestName builds a descriptive name for a generically named file,
// keeping its extension: "Screenshot 2026-10-21 14.03.22.png",
// "Photo 2026-10-21 11.36.48.jpg", "Artist - Song.mp3", "Annual Report.pdf".
func SuggestName(in Info) string {
	if in.Title != "" {
		return SafeFileName(in.Title) + in.Ext
	}
	stamp := in.Date.Format("2006-01-02 15.04.05")
	var label string
	switch {
	case in.Kind == "screenshot":
		label = "Screenshot"
	case in.Kind == "recording":
		label = "Screen recording"
	case in.Kind == "camera" && in.Type == "video":
		label = "Video"
	case in.Kind == "camera":
		label = "Photo"
	case typeLabel[in.Type] != "":
		label = typeLabel[in.Type]
	default:
		label = "File"
	}
	return label + " " + stamp + in.Ext
}

// SafeFileName removes characters that aren't allowed in file names on any
// of Windows, macOS or Linux, and keeps names a sensible length.
func SafeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if r := []rune(s); len(r) > 100 {
		s = strings.TrimSpace(string(r[:100]))
	}
	if s == "" {
		s = "File"
	}
	return s
}
