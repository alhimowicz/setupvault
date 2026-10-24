package tui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func TestLongNoticesWrapWithoutDroppingText(t *testing.T) {
	for _, width := range []int{28, 50, 80} {
		for _, failed := range []bool{false, true} {
			m := testModel(t)
			m.width = width
			m.refreshing = false
			m.failed = failed
			m.message = "NOTICESTART A long message with a path /" + strings.Repeat("verylongsegment", 12) + " and Unicode 世界界界. NOTICEEND"
			view := ansi.Strip(m.View().Content)
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("line overflow at width %d: %q", width, line)
				}
			}
			start := strings.Index(view, "NOTICESTART")
			end := strings.Index(view, "NOTICEEND")
			if start < 0 || end < 0 {
				t.Fatal("notice was clipped", view)
			}
			stripSpace := func(s string) string {
				return strings.Map(func(r rune) rune {
					if unicode.IsSpace(r) {
						return -1
					}
					return r
				}, s)
			}
			if stripSpace(view[start:end+len("NOTICEEND")]) != stripSpace(m.message) {
				t.Fatal("wrapped notice lost text", view)
			}
		}
	}
}
