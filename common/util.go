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

// TODO: replace with cmp.Or after go1.22
// Or returns the first of its arguments that is not equal to the zero value.
// If no argument is non-zero, it returns the zero value.
func Or[T comparable](vals ...T) T {
	var zero T
	for _, val := range vals {
		if val != zero {
			return val
		}
	}
	return zero
}
