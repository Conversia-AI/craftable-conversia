package tts

import "context"

// TTSClient defines the contract for text-to-speech providers.
type TTSClient interface {
	Synthesize(ctx context.Context, req SynthesizeRequest) (SynthesizeResult, error)
}

// SynthesizeRequest contains the text and synthesis parameters for a TTS call.
type SynthesizeRequest struct {
	// Text to synthesize. Required; max 4096 characters.
	Text string

	// Voice selects the speaker voice.
	// Supported: alloy, ash, ballad, coral, echo, fable, nova, onyx, sage, shimmer, verse.
	// Defaults to "nova" if empty.
	Voice string

	// Model selects the TTS model.
	// Supported: tts-1, tts-1-hd.
	// Defaults to "tts-1" if empty.
	Model string

	// OutputFormat selects the audio encoding of the returned buffer.
	// Supported: mp3, opus, aac, flac, wav, pcm.
	// Defaults to "opus" if empty.
	OutputFormat string

	// Context fields used for audit logging only; not sent to OpenAI.
	OrgUnitID     string
	SessionID     string
	IntegrationID string
}

// SynthesizeResult contains the audio buffer and metadata returned by a TTS provider.
type SynthesizeResult struct {
	AudioData []byte

	// CharacterCount is the number of characters in the synthesized text.
	CharacterCount int

	// OutputFormat is the audio encoding of AudioData (mp3, opus, etc.).
	OutputFormat string

	// OutputDurationSeconds is an estimated duration of the generated audio.
	OutputDurationSeconds float64

	Model     string
	Voice     string
	LatencyMs int

	AuditData TTSAuditData
}

// TTSAuditData carries provider metadata needed by upstream audit logging.
type TTSAuditData struct {
	Provider       string
	RequestID      string
	CharacterCount int
	OutputFormat   string
	ErrorCode      string
	ErrorMessage   string
}
