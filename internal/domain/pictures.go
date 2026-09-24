package domain

import (
	"strings"
	"time"
	"unicode"
)

// MaxPictures is how many pictures one done post carries at most, the
// size of a Telegram album.
const MaxPictures = 10

// maxPictureRefs bounds how many candidate paths one reply yields, so a
// reply that lists a whole folder costs a bounded number of file checks.
const maxPictureRefs = 100

// Picture is one image file an agent's reply names, read for its topic.
// Path is where it was read, Name its base name, Modified when it was last
// written. Photo marks a PNG or JPEG Telegram may show as a photo; any
// other picture (a GIF, a WebP, a full-page screenshot Telegram would
// shrink past reading) is uploaded as a file, untouched.
type Picture struct {
	Path     string
	Name     string
	Data     []byte
	Modified time.Time
	Photo    bool
}

// pictureExts are the file endings PictureRefs looks for, lower case.
var pictureExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

// PictureRefs returns the paths in text that end in a picture extension,
// in order and without repeats, as the reply wrote them: absolute,
// relative or starting with "~/". A path is cut out of the surrounding
// prose at spaces, quotes, brackets, Markdown emphasis and Chinese
// punctuation; the content of an inline code span is also tried whole, so
// a path with spaces can be named in backticks. URLs are skipped. The
// result only proposes paths; whether a file is there, is a picture and
// is recent enough is the PictureSource's to decide.
func PictureRefs(text string) []string {
	var refs []string
	seen := map[string]bool{}
	add := func(c string) {
		c = strings.TrimRight(strings.TrimSpace(c), trailingRefPunct)
		if c == "" || seen[c] || len(refs) == maxPictureRefs || strings.Contains(c, "://") || !hasPictureExt(c) {
			return
		}
		seen[c] = true
		refs = append(refs, c)
	}
	for i, seg := range strings.Split(text, "`") {
		if i%2 == 1 && !strings.Contains(seg, "\n") {
			add(seg)
		}
		for _, tok := range strings.FieldsFunc(seg, isRefSeparator) {
			if strings.Contains(tok, "://") {
				continue
			}
			for _, part := range strings.FieldsFunc(tok, isColon) {
				add(part)
			}
		}
	}
	return refs
}

// trailingRefPunct is what ends a sentence right after a path.
const trailingRefPunct = ".,;!?…"

// isRefSeparator reports the runes that never belong to a path the way
// agents write them: white space, quotes, brackets, table bars, Markdown
// emphasis and Chinese punctuation. A colon is split separately, after
// URLs are dropped.
func isRefSeparator(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	return strings.ContainsRune("\"'()[]{}<>|,;*，。；、（）「」『』《》【】“”‘’！？", r)
}

func isColon(r rune) bool { return r == ':' || r == '：' }

// hasPictureExt reports whether p ends in a picture extension with a name
// in front of it.
func hasPictureExt(p string) bool {
	lower := strings.ToLower(p)
	base := lower[strings.LastIndexAny(lower, `/\`)+1:]
	for _, ext := range pictureExts {
		if strings.HasSuffix(base, ext) && len(base) > len(ext) {
			return true
		}
	}
	return false
}
