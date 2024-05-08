package common

import "fmt"

type HttpError int

func (e HttpError) Error() string {
	return fmt.Sprintf("<http status code %d>", e)
}
