package imgix

import (
	"crypto/md5" // #nosec G501 -- imgix's signature protocol requires MD5.
	"crypto/subtle"
	"encoding/hex"
	"net/url"
	"strings"
)

// VerifySignature checks md5(key + path + "?" + query) where query is the raw
// query string with only s removed (expires stays in the signed query).
func VerifySignature(u url.URL, key string) bool {
	if key == "" {
		return true
	}
	var signature string
	count := 0
	parts := strings.Split(u.RawQuery, "&")
	remaining := make([]string, 0, len(parts))
	for _, part := range parts {
		name, value, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			return false
		}
		if decoded == "s" {
			count++
			signature, err = url.QueryUnescape(value)
			if err != nil {
				return false
			}
		} else {
			remaining = append(remaining, part)
		}
	}
	if count != 1 {
		return false
	}
	// Clients sign the raw object key (not URL-encoded), so only
	// the decoded path is checked. Accepting the escaped form as well would let
	// a signature for /a%20b.pdf also validate /a%2520b.pdf, a different key.
	input := key + u.Path
	if query := strings.Join(remaining, "&"); query != "" {
		input += "?" + query
	}
	digest := md5.Sum([]byte(input)) // #nosec G401 -- compatibility protocol, not a new signing scheme.
	expected := hex.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}
