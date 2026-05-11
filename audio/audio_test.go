package audio

import "testing"

func TestNewOpenAISTTClient(t *testing.T) {
	t.Parallel()

	client := NewOpenAISTTClient("test-key")
	if client == nil {
		t.Fatal("expected non-nil STT client")
	}
}

func TestNewOpenAITTSClient(t *testing.T) {
	t.Parallel()

	client := NewOpenAITTSClient("test-key")
	if client == nil {
		t.Fatal("expected non-nil TTS client")
	}
}
