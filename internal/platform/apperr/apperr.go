// Package apperr adalah satu-satunya tempat error domain didefinisikan bersama HTTP status
// dan `code`-nya. Sumber kebenaran: docs/design/12-api-error-catalogue.md. Kode lain
// mengembalikan *apperr.Error (atau membungkusnya); lapisan HTTP mengubahnya menjadi RFC 9457.
package apperr

import (
	"errors"
	"net/http"
)

// FieldError adalah kesalahan per field untuk VALIDATION_ERROR.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error adalah error domain yang aman dikirim ke client.
type Error struct {
	Status int
	Code   string
	Title  string
	Detail string
	Fields []FieldError
	cause  error // tidak pernah dikirim ke client; hanya untuk log
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.cause.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.cause }

// Is membuat errors.Is(err, apperr.ErrNotFound) cocok berdasarkan Code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// WithDetail mengembalikan salinan dengan detail berbeda.
func (e *Error) WithDetail(detail string) *Error {
	c := *e
	c.Detail = detail
	return &c
}

// WithCause melampirkan penyebab internal (untuk log), tanpa mengubah apa yang dilihat client.
func (e *Error) WithCause(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

func def(status int, code, title string) *Error {
	return &Error{Status: status, Code: code, Title: title}
}

// Generic (katalog §1).
var (
	ErrValidation         = def(http.StatusBadRequest, "VALIDATION_ERROR", "Validation failed")
	ErrMalformedJSON      = def(http.StatusBadRequest, "MALFORMED_JSON", "Malformed JSON body")
	ErrUnauthenticated    = def(http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required")
	ErrTokenExpired       = def(http.StatusUnauthorized, "TOKEN_EXPIRED", "Access token expired")
	ErrInvalidCredentials = def(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password")
	ErrForbidden          = def(http.StatusForbidden, "FORBIDDEN", "Action not allowed")
	ErrNotFound           = def(http.StatusNotFound, "NOT_FOUND", "Resource not found")
	ErrResourceBusy       = def(http.StatusConflict, "RESOURCE_BUSY", "Resource is busy, retry later")
	ErrEmailRegistered    = def(http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "Email already registered")
	ErrPayloadTooLarge    = def(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body too large")
	ErrUnsupportedMedia   = def(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Unsupported media type")
	ErrInternal           = def(http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
	ErrUnavailable        = def(http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Service temporarily unavailable")
)

// Validation membuat VALIDATION_ERROR dengan daftar field yang salah.
func Validation(fields ...FieldError) *Error {
	e := *ErrValidation
	e.Fields = fields
	return &e
}

// As mengubah error apa pun menjadi *Error. Error yang tidak dikenal menjadi INTERNAL_ERROR
// dengan penyebab asli disimpan untuk log; pesan internal tidak pernah bocor ke client.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal.WithCause(err)
}
