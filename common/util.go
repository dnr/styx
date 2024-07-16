package common

import (
	"context"
	"encoding/base64"
	"errors"
)

func DigestStr(digest []byte) string {
	return base64.RawURLEncoding.EncodeToString(digest)
}

func ValOrErr[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

func IsContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
