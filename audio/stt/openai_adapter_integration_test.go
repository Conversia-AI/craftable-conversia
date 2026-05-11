package stt

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const (
	testOpenAIAPIKeyEnv = "OPENAI_API_KEY"
	testOGGFileEnv      = "CRAFTABLE_STT_TEST_OGG_FILE"
	testMP4FileEnv      = "CRAFTABLE_STT_TEST_MP4_FILE"
)

func TestOpenAIAdapterTranscribeOGGIntegration(t *testing.T) {
	t.Parallel()

	apiKey, filePath := requireIntegrationInputs(t, testOGGFileEnv)
	audioData := readFixtureFile(t, filePath)

	client := NewOpenAIAdapter(apiKey)
	result, err := client.Transcribe(context.Background(), TranscribeRequest{
		AudioData:     audioData,
		MimeType:      "audio/ogg; codecs=opus",
		Language:      "es",
		OrgUnitID:     "org_test",
		SessionID:     "session_test_ogg",
		IntegrationID: "integration_test_ogg",
	})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	assertSuccessfulTranscription(t, result, "audio/ogg; codecs=opus")
}

func TestOpenAIAdapterTranscribeMP4Integration(t *testing.T) {
	t.Parallel()

	apiKey, filePath := requireIntegrationInputs(t, testMP4FileEnv)
	audioData := readFixtureFile(t, filePath)

	client := NewOpenAIAdapter(apiKey)
	result, err := client.Transcribe(context.Background(), TranscribeRequest{
		AudioData:     audioData,
		MimeType:      "audio/mp4",
		Language:      "es",
		OrgUnitID:     "org_test",
		SessionID:     "session_test_mp4",
		IntegrationID: "integration_test_mp4",
	})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}

	assertSuccessfulTranscription(t, result, "audio/mp4")
}

func TestOpenAIAdapterTranscribeFileTooLarge(t *testing.T) {
	t.Parallel()

	client := NewOpenAIAdapter("test-key")
	audioData := make([]byte, defaultMaxFileSize+1)

	_, err := client.Transcribe(context.Background(), TranscribeRequest{
		AudioData: audioData,
		MimeType:  "audio/ogg; codecs=opus",
	})
	if err == nil {
		t.Fatal("expected ErrFileTooLarge")
	}
	if !IsErrFileTooLarge(err) {
		t.Fatalf("expected ErrFileTooLarge, got %T", err)
	}
}

func TestOpenAIAdapterInvalidAPIKeyIntegration(t *testing.T) {
	t.Parallel()

	filePath := requireFixturePath(t, testOGGFileEnv)
	audioData := readFixtureFile(t, filePath)

	client := NewOpenAIAdapter("invalid-api-key")
	_, err := client.Transcribe(context.Background(), TranscribeRequest{
		AudioData: audioData,
		MimeType:  "audio/ogg; codecs=opus",
		Language:  "es",
	})
	if err == nil {
		t.Fatal("expected typed STT error for invalid api key")
	}
	if !IsErrOpenAISTT(err) {
		t.Fatalf("expected ErrOpenAISTT, got %T", err)
	}

	var openAIErr *ErrOpenAISTT
	if !errors.As(err, &openAIErr) {
		t.Fatalf("expected *ErrOpenAISTT, got %T", err)
	}
	if openAIErr.StatusCode != 401 && openAIErr.StatusCode != 403 {
		t.Fatalf("expected unauthorized status code, got %d", openAIErr.StatusCode)
	}
}

func requireIntegrationInputs(t *testing.T, fileEnv string) (apiKey string, filePath string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping OpenAI integration test in short mode")
	}

	apiKey = strings.TrimSpace(os.Getenv(testOpenAIAPIKeyEnv))
	if apiKey == "" {
		t.Skipf("skipping integration test: %s is not set", testOpenAIAPIKeyEnv)
	}

	filePath = requireFixturePath(t, fileEnv)
	return apiKey, filePath
}

func requireFixturePath(t *testing.T, fileEnv string) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping OpenAI integration test in short mode")
	}

	filePath := strings.TrimSpace(os.Getenv(fileEnv))
	if filePath == "" {
		t.Skipf("skipping integration test: %s is not set", fileEnv)
	}

	return filePath
}

func readFixtureFile(t *testing.T, filePath string) []byte {
	t.Helper()

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", filePath, err)
	}
	if len(data) == 0 {
		t.Fatalf("fixture file %s is empty", filePath)
	}
	return data
}

func assertSuccessfulTranscription(t *testing.T, result TranscribeResult, expectedMimeType string) {
	t.Helper()

	if strings.TrimSpace(result.Text) == "" {
		t.Fatal("expected non-empty transcription text")
	}
	if result.Model != string(defaultModel) {
		t.Fatalf("expected model %q, got %q", defaultModel, result.Model)
	}
	if result.LatencyMs <= 0 {
		t.Fatalf("expected positive latency, got %d", result.LatencyMs)
	}
	if result.DurationSeconds < 0 {
		t.Fatalf("expected non-negative duration, got %f", result.DurationSeconds)
	}
	if result.AuditData.Provider != defaultProvider {
		t.Fatalf("expected provider %q, got %q", defaultProvider, result.AuditData.Provider)
	}
	if result.AuditData.AudioMimeType != expectedMimeType {
		t.Fatalf("expected audio mime type %q, got %q", expectedMimeType, result.AuditData.AudioMimeType)
	}
	if result.AuditData.AudioSizeBytes <= 0 {
		t.Fatalf("expected positive audio size, got %d", result.AuditData.AudioSizeBytes)
	}
}
