package common

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/avast/retry-go/v4"
)

func RetryHttpRequest(ctx context.Context, method, url, cType string, body []byte) (*http.Response, error) {
	return retry.DoWithData(
		func() (*http.Response, error) {
			var bReader io.Reader
			if body != nil {
				bReader = bytes.NewReader(body)
			}
			req, err := http.NewRequestWithContext(ctx, method, url, bReader)
			if err != nil {
				return nil, retry.Unrecoverable(err)
			}
			if cType != "" {
				req.Header.Set("Content-Type", cType)
			}
			res, err := http.DefaultClient.Do(req)
			if err == nil && res.StatusCode != http.StatusOK {
				err = HttpErrorFromRes(res)
				res.Body.Close()
			}
			return ValOrErr(res, err)
		},
		retry.Context(ctx),
		retry.UntilSucceeded(),
		retry.Delay(time.Second),
		retry.RetryIf(func(err error) bool {
			// retry on err or some 50x codes
			if status, ok := err.(HttpError); ok {
				switch status.Code() {
				case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
					return true
				default:
					return false
				}
			} else if IsContextError(err) {
				return false
			}
			return true
		}),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("http error (%d): %v, retrying", n, err)
		}))
}
