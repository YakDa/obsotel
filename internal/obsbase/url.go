package obs

import "net/url"

// SafeURLString returns a logging-safe form of u: scheme + host + path,
// with query, fragment, and userinfo stripped. Query strings commonly
// carry secrets (api_key, token, signature) and must never appear in
// logs by default.
//
// Callers who need the full URL for debugging should add it as their
// own attr; we deliberately don't surface it here because this is a
// contract package and the safe-by-default rule beats the convenience-
// of-default for the rare legitimate use case.
//
// Notes:
//   - Userinfo (user:pass@) is also stripped because URLs in logs
//     commonly embed credentials that way.
//   - If u is nil, returns "".
func SafeURLString(u *url.URL) string {
	if u == nil {
		return ""
	}
	// Build a copy so we don't mutate the caller's *url.URL.
	cp := *u
	cp.RawQuery = ""
	cp.Fragment = ""
	cp.User = nil
	return cp.String()
}
