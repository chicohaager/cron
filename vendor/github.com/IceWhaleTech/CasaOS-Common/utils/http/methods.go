// methods in this package automatically include a context with a timeout, in order to solve the problem of hanging requests and to avoid goroutine leaks
package http

import (
	"bytes"
	"context"
	"net/http"
	"time"
)

func Do(requestFunc func(ctx context.Context) (*http.Request, error), timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer func() {
		if timeout == 0 {
			// cancel immediately when this func returns, if timeout is not set
			cancel()
		}
	}()

	request, err := requestFunc(ctx)
	if err != nil {
		return nil, err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func Get(url string, timeout time.Duration) (*http.Response, error) {
	return GetWithHeader(url, timeout, nil)
}

func GetWithHeader(url string, timeout time.Duration, header map[string]string) (*http.Response, error) {
	return Do(func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		for k, v := range header {
			request.Header.Set(k, v)
		}
		return request, nil
	}, timeout)
}

// default header "Content-Type: application/json" is included.
func Post(url string, body []byte, timeout time.Duration) (*http.Response, error) {
	return PostWithHeader(url, body, timeout, nil)
}

// default header "Content-Type: application/json" is included.
func PostWithHeader(url string, body []byte, timeout time.Duration, header map[string]string) (*http.Response, error) {
	return Do(func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}

		request.Header.Set("Content-Type", "application/json")

		for k, v := range header {
			request.Header.Set(k, v)
		}

		return request, nil
	}, timeout)
}

// default header "Content-Type: application/json" is included.
func Put(url string, body []byte, timeout time.Duration) (*http.Response, error) {
	return Do(func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("accept", "application/json")
		request.Header.Set("Content-Type", "application/json")

		return request, nil
	}, timeout)
}
func PutByReader(url string, body []byte, timeout time.Duration) (*http.Response, error) {
	return Do(func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("accept", "application/json")
		request.Header.Set("Content-Type", "application/json")

		return request, nil
	}, timeout)
}

// default header "Content-Type: application/json" is included.
func Delete(url string, body []byte, timeout time.Duration) (*http.Response, error) {
	return Do(func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}

		request.Header.Set("Content-Type", "application/json")

		return request, nil
	}, timeout)
}
