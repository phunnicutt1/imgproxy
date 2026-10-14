package imgix_test

import (
	"crypto/md5"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/imgproxy/imgproxy/v4/handlers/imgix"
	"github.com/stretchr/testify/require"
)

func sign(path, query string) string {
	input := "secret" + path
	if query != "" {
		input += "?" + query
	}
	return fmt.Sprintf("%x", md5.Sum([]byte(input)))
}

func TestSignature(t *testing.T) {
	path := "/a%20b+%E9%9B%AA%2Fkey.pdf"
	// Clients sign the raw (decoded) object key.
	dec := "/a b+雪/key.pdf"
	for _, tc := range []struct {
		name, query string
		valid       bool
	}{
		{"correct", "page=2&w=300&s=" + sign(dec, "page=2&w=300"), true},
		{"wrong", "w=300&s=wrong", false}, {"missing", "w=300", false},
		{"only signature", "s=" + sign(dec, ""), true},
		{"first", "s=" + sign(dec, "w=300&page=2") + "&w=300&page=2", true},
		{"middle", "w=300&s=" + sign(dec, "w=300&page=2") + "&page=2", true},
		{"encoded key", "%73=" + sign(dec, "w=300") + "&w=300", true},
		{"raw encoding", "foo=a+b&foo=a%20b&x=%2f&s=" + sign(dec, "foo=a+b&foo=a%20b&x=%2f"), true},
		{"empty component", "foo=&&s=" + sign(dec, "foo=&&bar") + "&bar", true},
		{"reordered", "w=300&page=2&s=" + sign(dec, "page=2&w=300"), false},
		{"escaped path rejected", "s=" + sign(path, ""), false},
		{"expires is signed", "fit=max&fm=json&expires=1760457600&s=" + sign(dec, "fit=max&fm=json&expires=1760457600"), true},
		{"expires tampered", "fit=max&fm=json&expires=1760457601&s=" + sign(dec, "fit=max&fm=json&expires=1760457600"), false},
		{"duplicate", "s=" + sign(dec, "") + "&s=" + sign(dec, ""), false},
		{"uppercase", "s=" + strings.ToUpper(sign(dec, "")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(path + "?" + tc.query)
			require.NoError(t, err)
			require.Equal(t, tc.valid, imgix.VerifySignature(*u, "secret"))
			require.True(t, imgix.VerifySignature(*u, ""))
		})
	}
}

// A signature for one object must not validate another object whose escaped
// path decodes differently (/a%20b.pdf is "a b.pdf", /a%2520b.pdf is "a%20b.pdf").
func TestSignatureNoReplayAcrossKeys(t *testing.T) {
	s := sign("/a b.pdf", "w=10")
	ok, err := url.Parse("/a%20b.pdf?w=10&s=" + s)
	require.NoError(t, err)
	require.True(t, imgix.VerifySignature(*ok, "secret"))
	replay, err := url.Parse("/a%2520b.pdf?w=10&s=" + s)
	require.NoError(t, err)
	require.False(t, imgix.VerifySignature(*replay, "secret"))
	// The signature of the escaped spelling doesn't validate it either.
	replay, err = url.Parse("/a%2520b.pdf?w=10&s=" + sign("/a%2520b.pdf", "w=10"))
	require.NoError(t, err)
	require.False(t, imgix.VerifySignature(*replay, "secret"))
}
