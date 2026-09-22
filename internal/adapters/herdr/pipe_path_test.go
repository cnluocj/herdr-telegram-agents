package herdr

import (
	"testing"
)

func TestHerdrPipePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard windows absolute path with backslashes",
			input: `C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
			want:  `\\.\pipe\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
		},
		{
			name:  "windows path with forward slashes",
			input: `C:/Users/test/AppData/Roaming/herdr/herdr.sock`,
			want:  `\\.\pipe\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
		},
		{
			name:  "windows path with mixed slashes",
			input: `C:/Users/test\AppData/Roaming\herdr/herdr.sock`,
			want:  `\\.\pipe\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
		},
		{
			name:  "already fully qualified pipe path",
			input: `\\.\pipe\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
			want:  `\\.\pipe\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
		},
		{
			name:  "case insensitive pipe prefix",
			input: `\\.\PIPE\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
			want:  `\\.\PIPE\C:\Users\test\AppData\Roaming\herdr\herdr.sock`,
		},
		{
			name:  "short pipe path",
			input: `\\.\pipe\herdr.sock`,
			want:  `\\.\pipe\herdr.sock`,
		},
		{
			name:  "unc path anchored to local pipe namespace",
			input: `\\evil.com\share\herdr.sock`,
			want:  `\\.\pipe\evil.com\share\herdr.sock`,
		},
		{
			name:  "leading backslash stripped",
			input: `\herdr.sock`,
			want:  `\\.\pipe\herdr.sock`,
		},
		{
			name:  "leading forward slash stripped and normalized",
			input: `/herdr.sock`,
			want:  `\\.\pipe\herdr.sock`,
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace input",
			input: "   \t\n",
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := herdrPipePath(tc.input)
			if got != tc.want {
				t.Errorf("herdrPipePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
