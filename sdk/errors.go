package godark

// Error hierarchy mirrors python / rust / js. All concrete error types embed
// baseError so callers can type-assert via errors.As with a specific subtype:
//
//	var oe *godark.OrderError
//	if errors.As(err, &oe) {
//	    log.Printf("order rejected: code=%s reason=%s", oe.ErrorCode, oe.Message)
//	}

// baseError is the common payload every SDK error carries. It is unexported
// so the public-facing API is the concrete subtypes (OrderError, etc.). The
// struct cannot be named `Error` because that would shadow the Error() method
// added by errors.New-style errors when the field is accessed by name.
type baseError struct {
	// Kind is the category of error ("OrderError", "AuthenticationError",
	// ...); used by Error() formatting and as a programmatic discriminator.
	Kind string
	// Message is the human-readable description.
	Message string
}

// Error implements the standard error interface.
func (e *baseError) Error() string {
	if e.Kind == "" {
		return e.Message
	}
	return e.Kind + ": " + e.Message
}

// AuthenticationError is returned when API key authentication fails.
type AuthenticationError struct{ baseError }

func newAuthenticationError(msg string) *AuthenticationError {
	return &AuthenticationError{baseError{Kind: "AuthenticationError", Message: msg}}
}

// SessionError is returned when ECDH session setup or rekey fails.
type SessionError struct{ baseError }

func newSessionError(msg string) *SessionError {
	return &SessionError{baseError{Kind: "SessionError", Message: msg}}
}

// OrderError is returned when the sequencer rejects an order. ErrorCode
// carries the symbolic reason (e.g. "PRICE_DEVIATION_TOO_LARGE") when the
// server provided one; it's empty for free-form rejects.
type OrderError struct {
	baseError
	// ErrorCode is the symbolic error code. Empty when the server emitted
	// a free-form rejection without a code.
	ErrorCode string
}

func newOrderError(msg, errorCode string) *OrderError {
	return &OrderError{
		baseError: baseError{Kind: "OrderError", Message: msg},
		ErrorCode: errorCode,
	}
}

// ConnectionError is returned on WebSocket transport-level failures.
type ConnectionError struct{ baseError }

func newConnectionError(msg string) *ConnectionError {
	return &ConnectionError{baseError{Kind: "ConnectionError", Message: msg}}
}

// EncryptionError is returned when AES-GCM encryption or decryption fails.
type EncryptionError struct{ baseError }

func newEncryptionError(msg string) *EncryptionError {
	return &EncryptionError{baseError{Kind: "EncryptionError", Message: msg}}
}

// TimeoutError is returned when a command does not receive an ack within the
// configured response window.
type TimeoutError struct{ baseError }

func newTimeoutError(msg string) *TimeoutError {
	return &TimeoutError{baseError{Kind: "TimeoutError", Message: msg}}
}
