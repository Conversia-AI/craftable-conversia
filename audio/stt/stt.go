package stt

import "context"

// STTClient defines the contract for speech-to-text providers.
type STTClient interface {
	Transcribe(ctx context.Context, req TranscribeRequest) (TranscribeResult, error)
}

// TranscribeRequest contains the input audio and context required for transcription.
type TranscribeRequest struct {
	AudioData     []byte
	MimeType      string
	Language      string
	OrgUnitID     string
	SessionID     string
	IntegrationID string
}

// TranscribeResult contains the normalized result returned by an STT provider.
type TranscribeResult struct {
	Text             string
	DetectedLanguage string
	DurationSeconds  float64
	Model            string
	LatencyMs        int
	AuditData        STTAuditData
}

// STTAuditData carries provider metadata needed by upstream audit logging.
type STTAuditData struct {
	Provider         string
	RequestID        string
	AudioSizeBytes   int
	AudioMimeType    string
	DetectedLanguage string
	ErrorCode        string
	ErrorMessage     string
}
