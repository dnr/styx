package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

func LoadFromFileOrHttpUrl(urlString string) ([]byte, error) {
	u, err := url.Parse(urlString)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		return os.ReadFile(u.Path)
	case "http", "https":
		res, err := http.Get(urlString)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http error: %s", res.Status)
		}
		return io.ReadAll(res.Body)
	default:
		return nil, errors.New("unknown scheme, must use file or http[s]")
	}
}
