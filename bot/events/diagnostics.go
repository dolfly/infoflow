package events

import "github.com/dolfly/infoflow/bot/internal/realtime"

// Diagnostic is the redacted lifecycle record shared by Bot event mechanisms.
type Diagnostic = realtime.Diagnostic

// Observer receives redacted Bot event lifecycle records.
type Observer = realtime.Observer

// DiagnosticHandler adapts a function to Observer.
type DiagnosticHandler = realtime.DiagnosticHandler

// RuntimeError is the closed, payload-free error reported by event mechanisms.
type RuntimeError = realtime.RuntimeError

// ErrorHandler receives safe runtime error categories.
type ErrorHandler = realtime.ErrorHandler
