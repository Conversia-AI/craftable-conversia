package tts

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const (
	defaultModel        = openai.SpeechModelTTS1
	defaultVoice        = "nova"
	defaultOutputFormat = "opus"
	defaultProvider     = "openai"
	defaultTimeout      = 15 * time.Second
	defaultMaxRetries   = 2

	speechOpName      = "openai tts synthesis"
	requestIDHeader   = "x-request-id"
	requestIDFallback = "request-id"

	// estimatedCharsPerSecond is used to derive an approximate audio duration from
	// character count when the provider does not return an explicit duration.
	estimatedCharsPerSecond = 15.0
)

var validVoices = map[string]bool{
	"alloy":   true,
	"ash":     true,
	"ballad":  true,
	"coral":   true,
	"echo":    true,
	"fable":   true,
	"nova":    true,
	"onyx":    true,
	"sage":    true,
	"shimmer": true,
	"verse":   true,
}

var validModels = map[string]bool{
	"tts-1":           true,
	"tts-1-hd":        true,
	"gpt-4o-mini-tts": true,
}

var validFormats = map[string]bool{
	"mp3":  true,
	"opus": true,
	"aac":  true,
	"flac": true,
	"wav":  true,
	"pcm":  true,
}

// OpenAIAdapter implements TTSClient using the OpenAI audio speech API.
type OpenAIAdapter struct {
	client         openai.Client
	requestTimeout time.Duration
	model          string
}

// NewOpenAIAdapter creates a TTS adapter backed by OpenAI.
func NewOpenAIAdapter(apiKey string, opts ...option.RequestOption) *OpenAIAdapter {
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(defaultMaxRetries),
	}
	clientOpts = append(clientOpts, opts...)

	return &OpenAIAdapter{
		client:         openai.NewClient(clientOpts...),
		requestTimeout: defaultTimeout,
		model:          defaultModel,
	}
}

// Synthesize converts text to audio using the OpenAI TTS API.
func (a *OpenAIAdapter) Synthesize(ctx context.Context, req SynthesizeRequest) (SynthesizeResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return SynthesizeResult{}, &ErrOpenAITTS{Message: "text is empty"}
	}

	voice, err := resolveVoice(req.Voice)
	if err != nil {
		return SynthesizeResult{}, err
	}

	model := resolveModel(req.Model, a.model)

	format, err := resolveOutputFormat(req.OutputFormat)
	if err != nil {
		return SynthesizeResult{}, err
	}

	params := openai.AudioSpeechNewParams{
		Input:          req.Text,
		Model:          openai.SpeechModel(model),
		Voice:          openai.AudioSpeechNewParamsVoice(voice),
		ResponseFormat: openai.AudioSpeechNewParamsResponseFormat(format),
	}

	callCtx, cancel := context.WithTimeout(ctx, a.requestTimeout)
	defer cancel()

	start := time.Now()
	resp, err := a.client.Audio.Speech.New(callCtx, params, option.WithRequestTimeout(a.requestTimeout))
	latencyMs := int(time.Since(start) / time.Millisecond)
	if err != nil {
		return SynthesizeResult{}, classifyError(err, speechOpName)
	}
	defer resp.Body.Close()

	audioData, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return SynthesizeResult{}, &ErrOpenAITTS{
			Message: "failed to read tts audio response body",
			Cause:   readErr,
		}
	}

	charCount := len([]rune(req.Text))
	outputDuration := float64(charCount) / estimatedCharsPerSecond
	requestID := extractRequestID(resp)

	return SynthesizeResult{
		AudioData:             audioData,
		CharacterCount:        charCount,
		OutputFormat:          format,
		OutputDurationSeconds: outputDuration,
		Model:                 model,
		Voice:                 voice,
		LatencyMs:             latencyMs,
		AuditData: TTSAuditData{
			Provider:       defaultProvider,
			RequestID:      requestID,
			CharacterCount: charCount,
			OutputFormat:   format,
		},
	}, nil
}

func resolveVoice(voice string) (string, error) {
	v := strings.TrimSpace(strings.ToLower(voice))
	if v == "" {
		return defaultVoice, nil
	}
	if !validVoices[v] {
		return "", &ErrInvalidVoice{Voice: voice}
	}
	return v, nil
}

func resolveModel(requested, fallback string) string {
	m := strings.TrimSpace(strings.ToLower(requested))
	if m == "" {
		return fallback
	}
	if validModels[m] {
		return m
	}
	return fallback
}

func resolveOutputFormat(format string) (string, error) {
	f := strings.TrimSpace(strings.ToLower(format))
	if f == "" {
		return defaultOutputFormat, nil
	}
	if !validFormats[f] {
		return "", &ErrInvalidOutputFormat{Format: format}
	}
	return f, nil
}

func classifyError(err error, operation string) error {
	if isTimeoutError(err) {
		return &ErrTimeout{Operation: operation, Cause: err}
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return &ErrOpenAITTS{
			Message:    ttsErrorMessage(apiErr.StatusCode),
			Code:       strings.TrimSpace(apiErr.Code),
			RequestID:  extractRequestID(apiErr.Response),
			StatusCode: apiErr.StatusCode,
			Cause:      err,
		}
	}

	return &ErrOpenAITTS{
		Message: "openai tts synthesis failed",
		Cause:   err,
	}
}

func ttsErrorMessage(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "openai rate limit exceeded during tts synthesis"
	case statusCode >= http.StatusInternalServerError:
		return "openai tts service is temporarily unavailable"
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusGatewayTimeout:
		return "openai tts request timed out"
	case statusCode == http.StatusBadRequest:
		return "openai rejected the tts synthesis request"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "openai credentials are not authorized for tts"
	default:
		return "openai tts synthesis failed"
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func extractRequestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if id := strings.TrimSpace(resp.Header.Get(requestIDHeader)); id != "" {
		return id
	}
	return strings.TrimSpace(resp.Header.Get(requestIDFallback))
}

var _ TTSClient = (*OpenAIAdapter)(nil)
