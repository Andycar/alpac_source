package transcodesvc

import (
	"strconv"
	"testing"
	"time"
)

// The signed start URL is the only thing standing between a PUBLIC transcode box and an open
// SSRF / CPU-abuse endpoint, so the sign↔verify contract gets explicit coverage.
func TestTranscodeSignVerify(t *testing.T) {
	const secret = "s3cr3t-key"
	const src = "/lite/pidtor/s0a1b2c3?tsid=1"
	exp := time.Now().Add(time.Hour).Unix()
	sig := transcodeSign(secret, src, exp)

	t.Run("valid", func(t *testing.T) {
		if !transcodeVerify(secret, src, strconv.FormatInt(exp, 10), sig) {
			t.Fatal("valid signature rejected")
		}
	})
	t.Run("tampered src", func(t *testing.T) {
		if transcodeVerify(secret, src+"x", strconv.FormatInt(exp, 10), sig) {
			t.Fatal("accepted a signature for a different src")
		}
	})
	t.Run("wrong secret", func(t *testing.T) {
		if transcodeVerify("other", src, strconv.FormatInt(exp, 10), sig) {
			t.Fatal("accepted a signature made with a different secret")
		}
	})
	t.Run("expired", func(t *testing.T) {
		past := time.Now().Add(-time.Minute).Unix()
		if transcodeVerify(secret, src, strconv.FormatInt(past, 10), transcodeSign(secret, src, past)) {
			t.Fatal("accepted an expired signature")
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		if transcodeVerify(secret, src, "", "") || transcodeVerify("", src, strconv.FormatInt(exp, 10), sig) {
			t.Fatal("accepted an empty exp/sig or empty secret")
		}
	})
}
