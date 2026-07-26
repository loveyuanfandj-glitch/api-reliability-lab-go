package product

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func SignOutboundWebhook(payload []byte, secret string, at time.Time) string {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	signature := hmacHex([]byte(timestamp+"."+string(payload)), secret)
	return "t=" + timestamp + ",v1=" + signature
}

func VerifyOutboundWebhook(signature string, payload []byte, secret string, now time.Time, tolerance time.Duration) error {
	return verifyTimestampedSignature(signature, payload, secret, now, tolerance)
}

func VerifyStripeSignature(signature string, payload []byte, secret string, now time.Time, tolerance time.Duration) error {
	return verifyTimestampedSignature(signature, payload, secret, now, tolerance)
}

func VerifyShopifySignature(signature string, payload []byte, secret string) error {
	provided, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

func verifyTimestampedSignature(header string, payload []byte, secret string, now time.Time, tolerance time.Duration) error {
	var timestamp int64
	var signatures []string
	for part := range strings.SplitSeq(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp, _ = strconv.ParseInt(value, 10, 64)
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp == 0 || len(signatures) == 0 {
		return ErrInvalidSignature
	}
	signedAt := time.Unix(timestamp, 0)
	if tolerance > 0 && (now.Sub(signedAt) > tolerance || signedAt.Sub(now) > tolerance) {
		return fmt.Errorf("%w: timestamp outside tolerance", ErrInvalidSignature)
	}
	expected, err := hex.DecodeString(hmacHex([]byte(strconv.FormatInt(timestamp, 10)+"."+string(payload)), secret))
	if err != nil {
		return ErrInvalidSignature
	}
	for _, signature := range signatures {
		provided, err := hex.DecodeString(signature)
		if err == nil && hmac.Equal(provided, expected) {
			return nil
		}
	}
	return ErrInvalidSignature
}

func hmacHex(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
