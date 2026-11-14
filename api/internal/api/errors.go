package api

// ErrorResponse matches the Error schema in api.yaml.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}
