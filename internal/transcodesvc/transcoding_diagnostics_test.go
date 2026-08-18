package transcodesvc

import (
	"strings"
	"testing"
)

func TestDiagnoseStartError_ConfigurationCases(t *testing.T) {
	cases := []struct {
		errMsg   string
		wantCode string
	}{
		{"Transcoding disabled", "transcoding_disabled"},
		{"Source host is not allowed", "host_not_allowed"},
		{"Only http/https URLs are allowed", "invalid_scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.errMsg, func(t *testing.T) {
			got := diagnoseStartError(tc.errMsg, "")
			if got.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tc.wantCode)
			}
			if got.UserMessage == "" {
				t.Errorf("UserMessage should not be empty")
			}
			if len(got.Suggestions) == 0 {
				t.Errorf("Suggestions should be populated")
			}
		})
	}
}

func TestDiagnoseStartError_SchedulerBusy(t *testing.T) {
	got := diagnoseStartError(ErrSchedulerBusy.Error(), "https://cdn.example.com/v.mkv")
	if got.Code != "scheduler_busy" {
		t.Errorf("Code = %q, want scheduler_busy", got.Code)
	}
	if !strings.Contains(strings.ToLower(got.UserMessage), "слот") {
		t.Errorf("UserMessage should mention слот, got %q", got.UserMessage)
	}
}

func TestDiagnoseStartError_DiskAndFFmpeg(t *testing.T) {
	cases := map[string]string{
		"Failed to create output directory":                                            "disk_unavailable",
		"Failed to start ffmpeg: exec: \"ffmpeg\": executable file not found in $PATH": "ffmpeg_not_found",
		"Failed to create stderr pipe: too many open files":                            "os_pipe_failure",
	}
	for in, wantCode := range cases {
		got := diagnoseStartError(in, "")
		if got.Code != wantCode {
			t.Errorf("%q → %q, want %q", in, got.Code, wantCode)
		}
	}
}

func TestDiagnoseStartError_HTTPErrors(t *testing.T) {
	cases := map[string]string{
		"403 Forbidden":            "source_blocked",
		"HTTP 404 Not Found":       "source_missing",
		"connection timed out":     "source_timeout",
		"connection reset by peer": "source_unreachable",
		"connection refused":       "source_unreachable",
	}
	for in, wantCode := range cases {
		got := diagnoseStartError(in, "")
		if got.Code != wantCode {
			t.Errorf("%q → %q, want %q", in, got.Code, wantCode)
		}
		if len(got.Suggestions) == 0 {
			t.Errorf("HTTP errors should have suggestions, got none for %q", in)
		}
	}
}

func TestDiagnoseStartError_GenericFallback(t *testing.T) {
	got := diagnoseStartError("some weird internal error", "")
	if got.Code != "transcoding_error" {
		t.Errorf("unknown error → %q, want transcoding_error", got.Code)
	}
	if got.UserMessage == "" {
		t.Errorf("fallback should still have a UserMessage")
	}
}

func TestDiagnoseFFmpegExit_HW(t *testing.T) {
	logs := []string{
		"ffmpeg version 5.1",
		"[h264_nvenc] Failed to initialize CUDA",
	}
	got := diagnoseFFmpegExit(1, logs)
	if got.Code != "ffmpeg_hw_failed" {
		t.Errorf("HW failure → %q, want ffmpeg_hw_failed", got.Code)
	}
	// User message should reassure that auto-fallback exists.
	if !strings.Contains(strings.ToLower(got.UserMessage), "cpu") {
		t.Errorf("HW UserMessage should mention CPU fallback, got %q", got.UserMessage)
	}
}

func TestDiagnoseFFmpegExit_Codec(t *testing.T) {
	logs := []string{"Could not find tag for codec eac3 in stream"}
	got := diagnoseFFmpegExit(1, logs)
	if got.Code != "ffmpeg_codec_refused" {
		t.Errorf("codec refused → %q, want ffmpeg_codec_refused", got.Code)
	}
}

func TestDiagnoseFFmpegExit_IO(t *testing.T) {
	logs := []string{"Server returned 502 Bad Gateway"}
	got := diagnoseFFmpegExit(1, logs)
	if got.Code != "ffmpeg_io_error" {
		t.Errorf("io error → %q, want ffmpeg_io_error", got.Code)
	}
}

func TestDiagnoseFFmpegExit_Unknown(t *testing.T) {
	got := diagnoseFFmpegExit(255, []string{"frame=12345 fps=120"})
	if got.Code != "ffmpeg_unknown_exit" {
		t.Errorf("unknown exit → %q, want ffmpeg_unknown_exit", got.Code)
	}
	if !strings.Contains(got.UserMessage, "255") {
		t.Errorf("UserMessage should include the exit code, got %q", got.UserMessage)
	}
}
