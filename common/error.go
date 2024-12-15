package common

import "fmt"

type HttpError struct {
	code int
	body string
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
