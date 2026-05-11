package stt

import "testing"

func TestNormalizeAudioMimeType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "ogg opus", input: "audio/ogg; codecs=opus", expected: "audio/ogg; codecs=opus"},
		{name: "mp4", input: "audio/mp4", expected: "audio/mp4"},
		{name: "m4a alias", input: "audio/x-m4a", expected: "audio/m4a"},
		{name: "mp3 alias", input: "audio/mp3", expected: "audio/mpeg"},
		{name: "wav alias", input: "audio/x-wav", expected: "audio/wav"},
		{name: "webm extension", input: "webm", expected: "audio/webm"},
		{name: "opus extension", input: "opus", expected: "audio/ogg; codecs=opus"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeAudioMimeType(tc.input)
			if err != nil {
				t.Fatalf("normalizeAudioMimeType() error = %v", err)
			}
			if got != tc.expected {
				t.Fatalf("normalizeAudioMimeType() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestNormalizeAudioMimeTypeUnsupported(t *testing.T) {
	t.Parallel()

	_, err := normalizeAudioMimeType("audio/flac")
	if err == nil {
		t.Fatal("expected unsupported format error")
	}

	if !IsErrUnsupportedFormat(err) {
		t.Fatalf("expected ErrUnsupportedFormat, got %T", err)
	}
}

func TestValidateAudioSize(t *testing.T) {
	t.Parallel()

	adapter := NewOpenAIAdapter("test-key")

	if err := adapter.validateAudioSize(0); err == nil {
		t.Fatal("expected empty payload error")
	}

	if err := adapter.validateAudioSize(adapter.maxFileSizeBytes + 1); err == nil {
		t.Fatal("expected file too large error")
	} else if !IsErrFileTooLarge(err) {
		t.Fatalf("expected ErrFileTooLarge, got %T", err)
	}
}
