package transcodesvc

import "testing"

func TestParseFFmpegStableMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		// Stable releases.
		{"ffmpeg version 7.1.1 Copyright (c) 2000-2024", 7},
		{"ffmpeg version 7.0 Copyright (c) 2000-2024", 7},
		{"ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023", 6},
		{"ffmpeg version 5.1.4 Copyright (c) 2000-2022", 5},

		// Distro-patched (Ubuntu 20.04 system ffmpeg — the user's case).
		{"ffmpeg version 4.2.7-0ubuntu0.1 Copyright (c) 2000-2022", 4},
		{"ffmpeg version 4.4.2-0ubuntu0.22.04.1 Copyright (c) 2000-2021", 4},

		// BtbN master nightly — version token starts with N, not a digit.
		// Stable parser must NOT match these (returns 0 → fallback fires).
		{"ffmpeg version N-118527-g0fabc9876b-20240315 Copyright (c) 2000-2024", 0},
		{"ffmpeg version n7.1-latest-...-something Copyright", 7}, // edge: tag-style starts with n+digit
		{"ffmpeg version git-master-abc123 Copyright", 0},

		// Malformed — defends against unexpected output.
		{"", 0},
		{"this is not ffmpeg output", 0},
	}
	for _, tc := range cases {
		if got := parseFFmpegStableMajor(tc.in); got != tc.want {
			t.Errorf("parseFFmpegStableMajor(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseFFmpegMajorFromLibavutil(t *testing.T) {
	// Real ffmpeg -version output snippets, captured from production builds.
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			"ubuntu 20.04 system ffmpeg 4.2.7",
			`ffmpeg version 4.2.7-0ubuntu0.1
libavutil      56. 31.100 / 56. 31.100
libavcodec     58. 54.100 / 58. 54.100`,
			4,
		},
		{
			"ffmpeg 5.x",
			`ffmpeg version 5.1.4
libavutil      57. 28.100 / 57. 28.100
libavcodec     59. 37.100 / 59. 37.100`,
			5,
		},
		{
			"ffmpeg 6.x",
			`ffmpeg version 6.1.1
libavutil      58. 29.100 / 58. 29.100
libavcodec     60. 31.102 / 60. 31.102`,
			6,
		},
		{
			"ffmpeg 7.x BtbN nightly",
			`ffmpeg version N-118527-g0fabc9876b-20240315 Copyright (c) 2000-2024 the FFmpeg developers
built with gcc 13 (Ubuntu 13.2.0-4ubuntu3)
configuration: --enable-gpl --enable-libx264 ...
libavutil      59. 12.100 / 59. 12.100
libavcodec     61.  3.100 / 61.  3.100
libavformat    61.  1.100 / 61.  1.100`,
			7,
		},
		{
			"future ffmpeg 8.x",
			`ffmpeg version 8.0
libavutil      60. 12.100 / 60. 12.100`,
			8,
		},
		{
			"missing libavutil — returns 0",
			`ffmpeg version foo
libavformat    61.  1.100 / 61.  1.100`,
			0,
		},
		{
			"empty input",
			``,
			0,
		},
		{
			"pre-4.0 ffmpeg with libavutil 55",
			`ffmpeg version 3.4.0
libavutil      55. 78.100 / 55. 78.100`,
			1, // flattened to "very old"
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFFmpegMajorFromLibavutil(tc.body); got != tc.want {
				t.Errorf("parseFFmpegMajorFromLibavutil(%q) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestParseFFmpeg_BtbNNightlyEnd2End captures the exact failure mode the
// user hit: BtbN nightly first line doesn't carry a numeric major, so the
// stable parser returns 0 and the libavutil fallback must kick in.
func TestParseFFmpeg_BtbNNightlyEnd2End(t *testing.T) {
	body := `ffmpeg version N-118527-g0fabc9876b-20240315 Copyright (c) 2000-2024 the FFmpeg developers
built with gcc 13 (Ubuntu 13.2.0-4ubuntu3) 20231024
configuration: --prefix=/ffbuild/prefix --pkg-config-flags=--static --pkg-config=pkg-config --cross-prefix=x86_64-w64-mingw32- --target-os=mingw32 --arch=x86_64 ...
libavutil      59. 12.100 / 59. 12.100
libavcodec     61.  3.100 / 61.  3.100
libavformat    61.  1.100 / 61.  1.100
libavdevice    61.  1.100 / 61.  1.100
libavfilter    10.  1.100 / 10.  1.100
libswscale      8.  1.100 /  8.  1.100
libswresample   5.  1.100 /  5.  1.100
libpostproc    58.  1.100 / 58.  1.100
`
	firstLine := "ffmpeg version N-118527-g0fabc9876b-20240315 Copyright (c) 2000-2024 the FFmpeg developers"
	stable := parseFFmpegStableMajor(firstLine)
	if stable != 0 {
		t.Errorf("BtbN nightly first line must NOT match stable parser, got %d", stable)
	}
	fallback := parseFFmpegMajorFromLibavutil(body)
	if fallback != 7 {
		t.Errorf("libavutil fallback for nightly = %d, want 7", fallback)
	}
}
