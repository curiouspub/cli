// Package controlwire describes the public browser-login exchange.
package controlwire

const (
	CapabilitiesPath = "/v1/auth/client/capabilities"
	RequestsPath     = "/v1/auth/client/requests"
	VerificationPath = "/client-login"
	DecisionPath     = "/v1/auth/client/decision"
	PollPath         = "/v1/auth/client/poll"
	SessionPath      = "/v1/auth/client/session"
	RevokePath       = "/v1/auth/client/revoke"
	IntervalSeconds  = 5
	MaxResponseBytes = 4096
)

type Endpoints struct {
	Capabilities string `json:"capabilities"`
	Requests     string `json:"requests"`
	Verification string `json:"verification"`
	Decision     string `json:"decision"`
	Poll         string `json:"poll"`
	Session      string `json:"session"`
	Revoke       string `json:"revoke"`
}
type Capabilities struct {
	Version         int       `json:"version"`
	ClientKinds     []string  `json:"client_kinds"`
	Endpoints       Endpoints `json:"endpoints"`
	IntervalSeconds int       `json:"interval_seconds"`
}
type CreateRequest struct {
	ProofSHA256 string `json:"proof_sha256"`
	ClientKind  string `json:"client_kind"`
}
type CreateResponse struct {
	RequestID               string `json:"request_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresAt               string `json:"expires_at"`
	IntervalSeconds         int    `json:"interval_seconds"`
}
type PollRequest struct {
	RequestID string `json:"request_id"`
	Proof     string `json:"proof"`
}
type IssuanceResponse struct {
	Credential   string `json:"credential"`
	CredentialID string `json:"credential_id"`
	UserID       string `json:"user_id"`
	ExpiresAt    string `json:"expires_at"`
}
type SessionResponse struct {
	CredentialID string `json:"credential_id"`
	UserID       string `json:"user_id"`
	ExpiresAt    string `json:"expires_at"`
}
type ErrorResponse struct {
	Error             string `json:"error"`
	RetryAfterSeconds *int   `json:"retry_after_seconds,omitempty"`
}
