package tts

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const testOpenAIAPIKeyEnv = "OPENAI_API_KEY"

// requireAPIKey skips the test when OPENAI_API_KEY is not set or -short is active.
func requireAPIKey(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping OpenAI integration test in short mode")
	}

	apiKey := strings.TrimSpace(os.Getenv(testOpenAIAPIKeyEnv))
	if apiKey == "" {
		t.Skipf("skipping integration test: %s is not set", testOpenAIAPIKeyEnv)
	}

	return apiKey
}

// assertSuccessfulSynthesis verifies the invariants that every successful TTS call must satisfy.
func assertSuccessfulSynthesis(t *testing.T, result SynthesizeResult, expectedVoice, expectedModel, expectedFormat string) {
	t.Helper()

	if len(result.AudioData) == 0 {
		t.Fatal("expected non-empty audio buffer")
	}
	if result.OutputFormat != expectedFormat {
		t.Fatalf("expected output format %q, got %q", expectedFormat, result.OutputFormat)
	}
	if result.Model != expectedModel {
		t.Fatalf("expected model %q, got %q", expectedModel, result.Model)
	}
	if result.Voice != expectedVoice {
		t.Fatalf("expected voice %q, got %q", expectedVoice, result.Voice)
	}
	if result.CharacterCount <= 0 {
		t.Fatalf("expected positive character count, got %d", result.CharacterCount)
	}
	if result.LatencyMs <= 0 {
		t.Fatalf("expected positive latency, got %d", result.LatencyMs)
	}
	if result.OutputDurationSeconds <= 0 {
		t.Fatalf("expected positive estimated duration, got %f", result.OutputDurationSeconds)
	}
	if result.AuditData.Provider != defaultProvider {
		t.Fatalf("expected provider %q, got %q", defaultProvider, result.AuditData.Provider)
	}
	if result.AuditData.OutputFormat != expectedFormat {
		t.Fatalf("expected audit output format %q, got %q", expectedFormat, result.AuditData.OutputFormat)
	}
	if result.AuditData.CharacterCount != result.CharacterCount {
		t.Fatalf("expected audit character count %d to match result character count %d",
			result.AuditData.CharacterCount, result.CharacterCount)
	}
}

// TestOpenAIAdapterSynthesizeOpusIntegration verifies end-to-end synthesis with
// the default voice (nova), default model (tts-1) and opus output format.
func TestOpenAIAdapterSynthesizeOpusIntegration(t *testing.T) {
	t.Parallel()

	apiKey := requireAPIKey(t)

	client := NewOpenAIAdapter(apiKey)
	result, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text:          "Hola, soy un agente de Conversia. En que puedo ayudarte hoy?",
		OutputFormat:  "opus",
		OrgUnitID:     "org_test",
		SessionID:     "session_test_opus",
		IntegrationID: "integration_test_opus",
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	assertSuccessfulSynthesis(t, result, defaultVoice, defaultModel, "opus")
}

// TestOpenAIAdapterSynthesizeVoiceAndModelIntegration verifies that explicitly
// setting voice=alloy and model=tts-1-hd also returns a valid audio buffer.
func TestOpenAIAdapterSynthesizeVoiceAndModelIntegration(t *testing.T) {
	t.Parallel()

	apiKey := requireAPIKey(t)

	client := NewOpenAIAdapter(apiKey)
	result, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text:          "Este es un mensaje de prueba con una voz y modelo distintos.",
		Voice:         "alloy",
		Model:         "tts-1-hd",
		OutputFormat:  "mp3",
		OrgUnitID:     "org_test",
		SessionID:     "session_test_hd",
		IntegrationID: "integration_test_hd",
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	assertSuccessfulSynthesis(t, result, "alloy", "tts-1-hd", "mp3")
}

// TestOpenAIAdapterInvalidAPIKeyIntegration verifies that an invalid API key
// is classified as ErrOpenAITTS with a 401 or 403 status code.
func TestOpenAIAdapterInvalidAPIKeyIntegration(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping OpenAI integration test in short mode")
	}

	client := NewOpenAIAdapter("invalid-api-key")
	_, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text:  "Test de autenticacion con clave invalida.",
		Voice: "nova",
	})
	if err == nil {
		t.Fatal("expected typed TTS error for invalid api key")
	}
	if !IsErrOpenAITTS(err) {
		t.Fatalf("expected ErrOpenAITTS, got %T: %v", err, err)
	}

	var openAIErr *ErrOpenAITTS
	if !errors.As(err, &openAIErr) {
		t.Fatalf("expected *ErrOpenAITTS, got %T", err)
	}
	if openAIErr.StatusCode != 401 && openAIErr.StatusCode != 403 {
		t.Fatalf("expected 401 or 403 status code, got %d", openAIErr.StatusCode)
	}
}

// TestOpenAIAdapterSynthesizeInvalidVoice verifies that an unrecognized voice
// is rejected locally as ErrInvalidVoice without calling the OpenAI API.
func TestOpenAIAdapterSynthesizeInvalidVoice(t *testing.T) {
	t.Parallel()

	client := NewOpenAIAdapter("test-key")
	_, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text:  "texto de prueba",
		Voice: "voz-inexistente",
	})
	if err == nil {
		t.Fatal("expected ErrInvalidVoice for unknown voice")
	}
	if !IsErrInvalidVoice(err) {
		t.Fatalf("expected ErrInvalidVoice, got %T: %v", err, err)
	}

	var voiceErr *ErrInvalidVoice
	if !errors.As(err, &voiceErr) {
		t.Fatalf("expected *ErrInvalidVoice, got %T", err)
	}
	if voiceErr.Voice != "voz-inexistente" {
		t.Fatalf("expected Voice=%q in error, got %q", "voz-inexistente", voiceErr.Voice)
	}
}

// TestOpenAIAdapterSynthesizeInvalidOutputFormat verifies that an unsupported
// output format is rejected locally as ErrInvalidOutputFormat.
func TestOpenAIAdapterSynthesizeInvalidOutputFormat(t *testing.T) {
	t.Parallel()

	client := NewOpenAIAdapter("test-key")
	_, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text:         "texto de prueba",
		OutputFormat: "formato-inexistente",
	})
	if err == nil {
		t.Fatal("expected ErrInvalidOutputFormat for unknown format")
	}
	if !IsErrInvalidOutputFormat(err) {
		t.Fatalf("expected ErrInvalidOutputFormat, got %T: %v", err, err)
	}

	var fmtErr *ErrInvalidOutputFormat
	if !errors.As(err, &fmtErr) {
		t.Fatalf("expected *ErrInvalidOutputFormat, got %T", err)
	}
	if fmtErr.Format != "formato-inexistente" {
		t.Fatalf("expected Format=%q in error, got %q", "formato-inexistente", fmtErr.Format)
	}
}

// TestOpenAIAdapterSynthesizeEmptyText verifies that an empty text input
// is rejected before reaching the OpenAI API.
func TestOpenAIAdapterSynthesizeEmptyText(t *testing.T) {
	t.Parallel()

	client := NewOpenAIAdapter("test-key")
	_, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text: "   ",
	})
	if err == nil {
		t.Fatal("expected error for empty text input")
	}
	if !IsErrOpenAITTS(err) {
		t.Fatalf("expected ErrOpenAITTS for empty text, got %T: %v", err, err)
	}
}

// TestOpenAIAdapterSynthesizeDefaultsIntegration verifies that leaving Voice,
// Model and OutputFormat empty falls back to the documented defaults.
func TestOpenAIAdapterSynthesizeDefaultsIntegration(t *testing.T) {
	t.Parallel()

	apiKey := requireAPIKey(t)

	client := NewOpenAIAdapter(apiKey)
	result, err := client.Synthesize(context.Background(), SynthesizeRequest{
		Text: "Texto corto para verificar los valores por defecto.",
	})
	if err != nil {
		t.Fatalf("Synthesize() with defaults error = %v", err)
	}

	assertSuccessfulSynthesis(t, result, defaultVoice, defaultModel, defaultOutputFormat)
}
