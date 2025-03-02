package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

type NotFoundable interface {
	IsNotFound() bool
}

func IsNotFound(err error) bool {
	var nf NotFoundable
	return errors.As(err, &nf) && nf.IsNotFound()
}

type HttpError struct {
	code int
	body string
}

var _ NotFoundable = (*HttpError)(nil)

// note: this does not close res.Body, caller should close it
func HttpErrorFromRes(res *http.Response) HttpError {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	return NewHttpError(res.StatusCode, string(body))
}

func NewHttpError(code int, body string) HttpError { return HttpError{code: code, body: body} }

func (e HttpError) Error() string {
	if len(e.body) == 0 {
		return fmt.Sprintf("http status %d", e.code)
	}
	return fmt.Sprintf("http status %d: %q", e.code, e.body)
}
func (e HttpError) Code() int    { return e.code }
func (e HttpError) Body() string { return e.body }
func (e HttpError) IsNotFound() bool {
	return e.code == http.StatusNotFound || e.code == http.StatusForbidden
}
