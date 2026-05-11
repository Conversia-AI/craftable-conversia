package stt

import (
	"errors"
	"fmt"
)

// ErrOpenAISTT wraps provider-specific transcription failures.
type ErrOpenAISTT struct {
	Message    string
	Code       string
	RequestID  string
	StatusCode int
	Cause      error
}

func (e *ErrOpenAISTT) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return fmt.Sprintf("openai stt failed: %v", e.Cause)
	}
	return "openai stt failed"
}

func (e *ErrOpenAISTT) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ErrUnsupportedFormat indicates the uploaded audio format is not supported.
type ErrUnsupportedFormat struct {
	MimeType string
}

func (e *ErrUnsupportedFormat) Error() string {
	if e == nil || e.MimeType == "" {
		return "unsupported audio format"
	}
	return fmt.Sprintf("unsupported audio format: %s", e.MimeType)
}

// ErrFileTooLarge indicates the uploaded audio exceeds the accepted size limit.
type ErrFileTooLarge struct {
	ActualBytes int
	MaxBytes    int
}

func (e *ErrFileTooLarge) Error() string {
	if e == nil {
		return "audio file is too large"
	}
	switch {
	case e.ActualBytes > 0 && e.MaxBytes > 0:
		return fmt.Sprintf("audio file is too large: %d bytes exceeds %d bytes", e.ActualBytes, e.MaxBytes)
	case e.MaxBytes > 0:
		return fmt.Sprintf("audio file exceeds maximum size of %d bytes", e.MaxBytes)
	default:
		return "audio file is too large"
	}
}

// ErrTimeout indicates the transcription request exceeded the configured timeout.
type ErrTimeout struct {
	Operation string
	Cause     error
}

func (e *ErrTimeout) Error() string {
	if e == nil {
		return "stt request timed out"
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s timed out", e.Operation)
	}
	return "stt request timed out"
}

func (e *ErrTimeout) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsErrOpenAISTT(err error) bool {
	var target *ErrOpenAISTT
	return errors.As(err, &target)
}

func IsErrUnsupportedFormat(err error) bool {
	var target *ErrUnsupportedFormat
	return errors.As(err, &target)
}

func IsErrFileTooLarge(err error) bool {
	var target *ErrFileTooLarge
	return errors.As(err, &target)
}

func IsErrTimeout(err error) bool {
	var target *ErrTimeout
	return errors.As(err, &target)
}
