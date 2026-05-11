package audio

import (
	"github.com/Conversia-AI/craftable-conversia/audio/stt"
	"github.com/Conversia-AI/craftable-conversia/audio/tts"
)

// --- STT ---

type STTClient = stt.STTClient
type TranscribeRequest = stt.TranscribeRequest
type TranscribeResult = stt.TranscribeResult
type STTAuditData = stt.STTAuditData

type ErrOpenAISTT = stt.ErrOpenAISTT
type ErrUnsupportedFormat = stt.ErrUnsupportedFormat
type ErrFileTooLarge = stt.ErrFileTooLarge

// ErrSTTTimeout is the timeout error type for STT operations.
type ErrSTTTimeout = stt.ErrTimeout

func NewOpenAISTTClient(apiKey string) STTClient {
	return stt.NewOpenAIAdapter(apiKey)
}

func IsErrOpenAISTT(err error) bool         { return stt.IsErrOpenAISTT(err) }
func IsErrUnsupportedFormat(err error) bool { return stt.IsErrUnsupportedFormat(err) }
func IsErrFileTooLarge(err error) bool      { return stt.IsErrFileTooLarge(err) }
func IsErrSTTTimeout(err error) bool        { return stt.IsErrTimeout(err) }

// IsErrTimeout checks for a timeout from either STT or TTS.
func IsErrTimeout(err error) bool {
	return stt.IsErrTimeout(err) || tts.IsErrTimeout(err)
}

// --- TTS ---

type TTSClient = tts.TTSClient
type SynthesizeRequest = tts.SynthesizeRequest
type SynthesizeResult = tts.SynthesizeResult
type TTSAuditData = tts.TTSAuditData

type ErrOpenAITTS = tts.ErrOpenAITTS
type ErrInvalidVoice = tts.ErrInvalidVoice
type ErrInvalidOutputFormat = tts.ErrInvalidOutputFormat

// ErrTTSTimeout is the timeout error type for TTS operations.
type ErrTTSTimeout = tts.ErrTimeout

func NewOpenAITTSClient(apiKey string) TTSClient {
	return tts.NewOpenAIAdapter(apiKey)
}

func IsErrOpenAITTS(err error) bool           { return tts.IsErrOpenAITTS(err) }
func IsErrInvalidVoice(err error) bool        { return tts.IsErrInvalidVoice(err) }
func IsErrInvalidOutputFormat(err error) bool { return tts.IsErrInvalidOutputFormat(err) }
func IsErrTTSTimeout(err error) bool          { return tts.IsErrTimeout(err) }
