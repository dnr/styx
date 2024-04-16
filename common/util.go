package common

import "encoding/base64"

func DigestStr(digest []byte) string {
	return base64.RawURLEncoding.EncodeToString(digest)
}
