package wetrakr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Error codes of the WeTrakr API that change how a failure is handled.
const (
	CodeQuotaExceeded        = "QUOTA_EXCEEDED"
	CodeAppQuotaExceeded     = "APP_QUOTA_EXCEEDED"
	CodeAppUserQuotaExceeded = "APP_USER_QUOTA_EXCEEDED"
	CodeConcurrencyLimit     = "CONCURRENCY_LIMIT"
	CodePlanLimitReached     = "PLAN_LIMIT_REACHED"
	CodeInvalidToken         = "INVALID_TOKEN"
	CodeTokenRevoked         = "TOKEN_REVOKED"
	CodeInvalidRefreshToken  = "INVALID_REFRESH_TOKEN"

	statusPlanLimit = 420
)

var quotaCodes = []string{CodeQuotaExceeded, CodeAppQuotaExceeded, CodeAppUserQuotaExceeded}

// APIError is a non-successful response of the WeTrakr API.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "wetrakr: %s %s: status %d", e.Method, e.Path, e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// IsQuota reports whether the error is a daily quota error, which no retry can fix.
func (e *APIError) IsQuota() bool {
	return e.Status == http.StatusTooManyRequests && slices.Contains(quotaCodes, e.Code)
}

// IsPlanLimit reports whether err is a plan limit error (420), such as too many lists or list items on the free plan.
func IsPlanLimit(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Status == statusPlanLimit || apiErr.Code == CodePlanLimitReached)
}

// NeedsReauth reports whether err means the user must authorize again: there is no usable token, or WeTrakr rejected
// both the access token and the refresh token (expired after 180 days unused, or revoked).
func NeedsReauth(err error) bool {
	if errors.Is(err, ErrNoToken) {
		return true
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusUnauthorized ||
		slices.Contains([]string{CodeInvalidRefreshToken, CodeInvalidToken, CodeTokenRevoked}, apiErr.Code)
}

// IsNotFound reports whether err is a 404 response.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// parseError reads the error body, which comes in three shapes:
// {"message"}, {"error":"CODE","message"} and {"error":{"code","message"}}.
func parseError(method, path string, status int, body []byte) *APIError {
	apiErr := &APIError{Method: method, Path: path, Status: status}
	var raw struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		apiErr.Message = strings.TrimSpace(truncate(string(body), 300))
		return apiErr
	}
	apiErr.Message = raw.Message
	if len(raw.Error) > 0 {
		var code string
		if err := json.Unmarshal(raw.Error, &code); err == nil {
			apiErr.Code = code
		} else {
			var obj struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw.Error, &obj); err == nil {
				apiErr.Code = obj.Code
				if apiErr.Message == "" {
					apiErr.Message = obj.Message
				}
			}
		}
	}
	return apiErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
