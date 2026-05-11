package tts

import (
	"errors"
	"fmt"
)

// ErrOpenAITTS wraps provider-specific TTS synthesis failures.
type ErrOpenAITTS struct {
	Message    string
	Code       string
	RequestID  string
	StatusCode int
	Cause      error
}

func (e *ErrOpenAITTS) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return fmt.Sprintf("openai tts failed: %v", e.Cause)
	}
	return "openai tts failed"
}

func (e *ErrOpenAITTS) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ErrInvalidVoice indicates an unsupported or unrecognized voice was requested.
type ErrInvalidVoice struct {
	Voice string
}

func (e *ErrInvalidVoice) Error() string {
	if e == nil || e.Voice == "" {
		return "invalid tts voice"
	}
	return fmt.Sprintf("invalid tts voice: %q", e.Voice)
}

// ErrInvalidOutputFormat indicates an unsupported audio output format was requested.
type ErrInvalidOutputFormat struct {
	Format string
}

func (e *ErrInvalidOutputFormat) Error() string {
	if e == nil || e.Format == "" {
		return "invalid tts output format"
	}
	return fmt.Sprintf("invalid tts output format: %q", e.Format)
}

// ErrTimeout indicates the TTS request exceeded the configured timeout.
type ErrTimeout struct {
	Operation string
	Cause     error
}

func (e *ErrTimeout) Error() string {
	if e == nil {
		return "tts request timed out"
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s timed out", e.Operation)
	}
	return "tts request timed out"
}

func (e *ErrTimeout) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsErrOpenAITTS(err error) bool {
	var target *ErrOpenAITTS
	return errors.As(err, &target)
}

func IsErrInvalidVoice(err error) bool {
	var target *ErrInvalidVoice
	return errors.As(err, &target)
}

func IsErrInvalidOutputFormat(err error) bool {
	var target *ErrInvalidOutputFormat
	return errors.As(err, &target)
}

func IsErrTimeout(err error) bool {
	var target *ErrTimeout
	return errors.As(err, &target)
}
