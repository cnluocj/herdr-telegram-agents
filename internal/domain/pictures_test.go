package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestPictureRefs(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"prose and punctuation": {
			"Saved the screenshot to /tmp/shot.png. Compare with ./before.JPG, then ~/Desktop/after.jpeg!",
			[]string{"/tmp/shot.png", "./before.JPG", "~/Desktop/after.jpeg"},
		},
		"chinese prose": {
			"截图：`.playwright-mcp/首页.png`，对比图在 docs/对比.webp。标签“/tmp/a.gif”",
			[]string{".playwright-mcp/首页.png", "docs/对比.webp", "/tmp/a.gif"},
		},
		"markdown image, link and emphasis": {
			"![home](shots/home.png \"Home\") see [the list](shots/list.png) and **shots/bold.png**",
			[]string{"shots/home.png", "shots/list.png", "shots/bold.png"},
		},
		"code span keeps spaces": {
			"open `/Users/me/My Shots/a b.png` now",
			// The pieces are tried too; only files that exist are sent.
			[]string{"/Users/me/My Shots/a b.png", "b.png"},
		},
		"label before a colon": {
			"截图:/tmp/x.png and Screenshot:shots/y.png",
			[]string{"/tmp/x.png", "shots/y.png"},
		},
		"urls are not paths": {
			"see https://example.com/a.png and file:///tmp/b.png and `http://localhost:5173/c.png`",
			nil,
		},
		"repeats and non-pictures": {
			"a.png a.png notes.txt .png /dir/.png image.pngx archive.png.zip a.png",
			[]string{"a.png"},
		},
		"table cells": {
			"| page | shot |\n|---|---|\n| home | shots/home.png |",
			[]string{"shots/home.png"},
		},
		"code fence": {
			"```\nls out/\nout/one.png out/two.png\n```",
			[]string{"out/one.png", "out/two.png"},
		},
		"nothing": {"All tests pass.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := PictureRefs(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PictureRefs =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestPictureRefsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3*maxPictureRefs; i++ {
		b.WriteString("shots/")
		b.WriteString(strings.Repeat("x", i%7+1))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("y", i/26))
		b.WriteString(".png ")
	}
	if got := PictureRefs(b.String()); len(got) != maxPictureRefs {
		t.Fatalf("refs = %d, want %d", len(got), maxPictureRefs)
	}
}
