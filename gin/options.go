package obsgin

import "github.com/YakDa/obsotel"

// RichOption configures optional rich fields on an HTTPError.
type RichOption func(*obsotel.HTTPError)

// WithCode sets a machine-readable error code (e.g. "case.not_found").
func WithCode(code string) RichOption {
	return func(he *obsotel.HTTPError) { he.Code = code }
}

// WithDetails sets the entire Details map, replacing any existing entries.
// A nil or empty map is a no-op.
func WithDetails(details map[string]any) RichOption {
	return func(he *obsotel.HTTPError) {
		if len(details) > 0 {
			he.Details = details
		}
	}
}

// WithDetail adds a single key-value pair to the Details map.
// An empty key is a no-op.
func WithDetail(key string, value any) RichOption {
	return func(he *obsotel.HTTPError) {
		if key == "" {
			return
		}
		if he.Details == nil {
			he.Details = map[string]any{}
		}
		he.Details[key] = value
	}
}

// WithRetryable marks the error as retryable (or not) in the client response.
func WithRetryable(retryable bool) RichOption {
	return func(he *obsotel.HTTPError) { he.Retryable = &retryable }
}
