package resolver

import (
	"strings"
	"testing"
)

func TestExtractURLFromInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain URL",
			input: "https://music.yandex.ru/users/yamusic-top/playlists/1076",
			want:  "https://music.yandex.ru/users/yamusic-top/playlists/1076",
		},
		{
			name:  "plain URL with leading and trailing spaces",
			input: "   https://music.yandex.ru/playlists/799426f4-b236-4d1d-9351-872f9be7aa8b   ",
			want:  "https://music.yandex.ru/playlists/799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:  "standard iframe with double quotes",
			input: `<iframe frameborder="0" style="border:none;width:100%;height:450px;" width="100%" height="450" src="https://music.yandex.ru/iframe/#playlist/yamusic-top/1076"></iframe>`,
			want:  "https://music.yandex.ru/iframe/#playlist/yamusic-top/1076",
		},
		{
			name:  "iframe with single quotes and UUID",
			input: `<iframe width="100%" height="450" src='https://music.yandex.ru/iframe/#playlist/799426f4-b236-4d1d-9351-872f9be7aa8b'></iframe>`,
			want:  "https://music.yandex.ru/iframe/#playlist/799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:  "iframe with protocol-relative link //",
			input: `<iframe src="//music.yandex.ru/iframe/#playlist/yamusic-top/1076"></iframe>`,
			want:  "https://music.yandex.ru/iframe/#playlist/yamusic-top/1076",
		},
		{
			name:  "iframe with uppercase SRC and spaces around equals",
			input: `<IFRAME frameborder="0" SRC = "https://music.yandex.ru/iframe/playlist/yamusic-top/1076" ></IFRAME>`,
			want:  "https://music.yandex.ru/iframe/playlist/yamusic-top/1076",
		},
		{
			name:  "iframe with html entity &amp; in src query",
			input: `<iframe src="https://music.yandex.ru/iframe/#playlist/yamusic-top/1076?lang=ru&amp;from=web"></iframe>`,
			want:  "https://music.yandex.ru/iframe/#playlist/yamusic-top/1076?lang=ru&from=web",
		},
		{
			name:  "iframe with newlines inside tag",
			input: "<iframe\n  width=\"100%\"\n  src=\"https://music.yandex.ru/iframe/#playlist/yamusic-top/1076\"\n></iframe>",
			want:  "https://music.yandex.ru/iframe/#playlist/yamusic-top/1076",
		},
		{
			name:  "malicious javascript scheme in iframe",
			input: `<iframe src="javascript:alert(1)"></iframe>`,
			want:  "",
		},
		{
			name:  "malicious data scheme in iframe",
			input: `<iframe src="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="></iframe>`,
			want:  "",
		},
		{
			name:  "malicious file scheme in iframe",
			input: `<iframe src="file:///etc/passwd"></iframe>`,
			want:  "",
		},
		{
			name:  "empty input",
			input: "   ",
			want:  "",
		},
		{
			name:  "excessively long input is bounded",
			input: "https://music.yandex.ru/playlists/" + strings.Repeat("a", 5000),
			want:  "https://music.yandex.ru/playlists/" + strings.Repeat("a", 4096-len("https://music.yandex.ru/playlists/")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractURLFromInput(tt.input)
			if got != tt.want {
				t.Errorf("ExtractURLFromInput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
