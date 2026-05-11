package stt

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// namedReader wraps a bytes.Reader and exposes a Name() method so the OpenAI
// SDK's multipart form encoder includes the filename with a proper extension.
// Without a valid extension Whisper returns 400 Bad Request.
type namedReader struct {
	*bytes.Reader
	name string
}

func (r *namedReader) Name() string { return r.name }

func filenameFromMimeType(mimeType string) string {
	switch {
	case strings.Contains(mimeType, "ogg"):
		return "audio.ogg"
	case strings.Contains(mimeType, "mp4"):
		return "audio.mp4"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"), strings.Contains(mimeType, "mpga"):
		return "audio.mp3"
	case strings.Contains(mimeType, "wav"):
		return "audio.wav"
	case strings.Contains(mimeType, "webm"):
		return "audio.webm"
	case strings.Contains(mimeType, "m4a"):
		return "audio.m4a"
	case strings.Contains(mimeType, "flac"):
		return "audio.flac"
	default:
		return "audio.ogg"
	}
}

const (
	defaultModel        = openai.AudioModelWhisper1
	defaultProvider     = "openai"
	defaultTimeout      = 30 * time.Second
	defaultMaxRetries   = 2
	defaultMaxFileSize  = 25 * 1024 * 1024
	requestIDHeader     = "x-request-id"
	requestIDFallback   = "request-id"
	transcriptionOpName = "openai stt transcription"
)

var supportedMimeTypes = map[string]string{
	"audio/m4a":   "audio/m4a",
	"audio/mp4":   "audio/mp4",
	"audio/mpeg":  "audio/mpeg",
	"audio/mpga":  "audio/mpeg",
	"audio/mp3":   "audio/mpeg",
	"audio/ogg":   "audio/ogg",
	"audio/opus":  "audio/ogg; codecs=opus",
	"audio/wav":   "audio/wav",
	"audio/wave":  "audio/wav",
	"audio/webm":  "audio/webm",
	"audio/x-m4a": "audio/m4a",
	"audio/x-wav": "audio/wav",
	"m4a":         "audio/m4a",
	"mp3":         "audio/mpeg",
	"mp4":         "audio/mp4",
	"mpga":        "audio/mpeg",
	"mpeg":        "audio/mpeg",
	"ogg":         "audio/ogg",
	"opus":        "audio/ogg; codecs=opus",
	"wav":         "audio/wav",
	"webm":        "audio/webm",
}

// OpenAIAdapter implements STTClient using the OpenAI audio transcription API.
type OpenAIAdapter struct {
	client           openai.Client
	requestTimeout   time.Duration
	maxFileSizeBytes int
	model            string
}

// NewOpenAIAdapter creates an STT adapter backed by OpenAI Whisper.
func NewOpenAIAdapter(apiKey string, opts ...option.RequestOption) *OpenAIAdapter {
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		// The SDK performs exponential backoff retries for connection errors, 429, and 5xx.
		option.WithMaxRetries(defaultMaxRetries),
	}
	clientOpts = append(clientOpts, opts...)

	return &OpenAIAdapter{
		client:           openai.NewClient(clientOpts...),
		requestTimeout:   defaultTimeout,
		maxFileSizeBytes: defaultMaxFileSize,
		model:            string(defaultModel),
	}
}

// Transcribe sends audio data to OpenAI Whisper and returns the normalized result.
func (a *OpenAIAdapter) Transcribe(ctx context.Context, req TranscribeRequest) (TranscribeResult, error) {
	if err := a.validateAudioSize(len(req.AudioData)); err != nil {
		return TranscribeResult{}, err
	}

	normalizedMimeType, err := normalizeAudioMimeType(req.MimeType)
	if err != nil {
		return TranscribeResult{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, a.requestTimeout)
	defer cancel()

	params := openai.AudioTranscriptionNewParams{
		File:           &namedReader{Reader: bytes.NewReader(req.AudioData), name: filenameFromMimeType(normalizedMimeType)},
		Model:          defaultModel,
		ResponseFormat: openai.AudioResponseFormatVerboseJSON,
	}
	if strings.TrimSpace(req.Language) != "" {
		params.Language = param.NewOpt(strings.TrimSpace(req.Language))
	}

	var rawResp *http.Response
	start := time.Now()
	resp, err := a.client.Audio.Transcriptions.New(
		callCtx,
		params,
		option.WithRequestTimeout(a.requestTimeout),
		option.WithResponseInto(&rawResp),
	)
	latencyMs := int(time.Since(start) / time.Millisecond)
	if err != nil {
		return TranscribeResult{}, a.classifyError(err, rawResp)
	}

	detectedLanguage := strings.TrimSpace(resp.Language)
	if detectedLanguage == "" {
		detectedLanguage = strings.TrimSpace(req.Language)
	}

	durationSeconds := resp.Duration
	if resp.Usage.Seconds > 0 {
		durationSeconds = resp.Usage.Seconds
	}

	return TranscribeResult{
		Text:             strings.TrimSpace(resp.Text),
		DetectedLanguage: detectedLanguage,
		DurationSeconds:  durationSeconds,
		Model:            a.model,
		LatencyMs:        latencyMs,
		AuditData: STTAuditData{
			Provider:         defaultProvider,
			RequestID:        extractRequestID(rawResp),
			AudioSizeBytes:   len(req.AudioData),
			AudioMimeType:    normalizedMimeType,
			DetectedLanguage: detectedLanguage,
		},
	}, nil
}

func (a *OpenAIAdapter) validateAudioSize(size int) error {
	if size <= 0 {
		return &ErrOpenAISTT{
			Message: "audio payload is empty",
		}
	}
	if size > a.maxFileSizeBytes {
		return &ErrFileTooLarge{
			ActualBytes: size,
			MaxBytes:    a.maxFileSizeBytes,
		}
	}
	return nil
}

func normalizeAudioMimeType(mimeType string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(mimeType))
	if normalized == "" {
		return "", &ErrUnsupportedFormat{MimeType: mimeType}
	}

	if supported, ok := supportedMimeTypes[normalized]; ok {
		return supported, nil
	}

	mediaType, params, err := mime.ParseMediaType(normalized)
	if err == nil {
		if mediaType == "audio/ogg" && strings.Contains(strings.ToLower(params["codecs"]), "opus") {
			return "audio/ogg; codecs=opus", nil
		}
		if supported, ok := supportedMimeTypes[mediaType]; ok {
			return supported, nil
		}
	}

	if mediaType == "" {
		mediaType = strings.TrimSpace(strings.Split(normalized, ";")[0])
		if supported, ok := supportedMimeTypes[mediaType]; ok {
			return supported, nil
		}
	}

	return "", &ErrUnsupportedFormat{MimeType: mimeType}
}

func (a *OpenAIAdapter) classifyError(err error, rawResp *http.Response) error {
	if isTimeoutError(err) {
		return &ErrTimeout{
			Operation: transcriptionOpName,
			Cause:     err,
		}
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return &ErrOpenAISTT{
			Message:    openAIErrorMessage(apiErr.StatusCode),
			Code:       strings.TrimSpace(apiErr.Code),
			RequestID:  extractRequestID(apiErr.Response),
			StatusCode: apiErr.StatusCode,
			Cause:      err,
		}
	}

	return &ErrOpenAISTT{
		Message:   "openai transcription failed",
		RequestID: extractRequestID(rawResp),
		Cause:     err,
	}
}

func openAIErrorMessage(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "openai rate limit exceeded during transcription"
	case statusCode >= http.StatusInternalServerError:
		return "openai transcription service is temporarily unavailable"
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusGatewayTimeout:
		return "openai transcription request timed out"
	case statusCode == http.StatusBadRequest:
		return "openai rejected the audio transcription request"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "openai credentials are not authorized for transcription"
	default:
		return "openai transcription failed"
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
	if requestID := strings.TrimSpace(resp.Header.Get(requestIDHeader)); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(resp.Header.Get(requestIDFallback))
}

var _ STTClient = (*OpenAIAdapter)(nil)
