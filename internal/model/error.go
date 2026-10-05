package model

// ErrorResponse is the shared JSON envelope for API errors.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
