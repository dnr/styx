package common

import (
	"context"
	"errors"
	"strings"
)

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

func NormalizeUpstream(u *string) {
	if !strings.HasSuffix(*u, "/") {
		// upstream should be a url pointing to a directory, so always use trailing-/ form.
		// nix drops the / even if it's present in nix.conf, so add it back here.
		*u = *u + "/"
	}
}
