package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InferenceRequest describes one raw Prism inference request. The caller owns
// response-body parsing because the four Prism APIs have different event and
// text shapes.
type InferenceRequest struct {
	API      string
	Provider string
	Body     []byte
}

const (
	InferenceConnectTimeout   = 10 * time.Second
	InferenceFirstByteTimeout = 30 * time.Second
	InferenceIdleTimeout      = 2 * time.Minute
)

var inferencePaths = map[string]string{
	"chat":        "/v1/chat/completions",
	"completions": "/v1/completions",
	"responses":   "/v1/responses",
	"messages":    "/v1/messages",
}

func InferencePath(apiName string) (string, bool) {
	path, ok := inferencePaths[apiName]
	return path, ok
}

// Inference sends a request without a total timeout. The default transport
// limits connection establishment and response headers; stream readers must
// enforce the idle timeout while consuming the body.
func (c Client) Inference(ctx context.Context, request InferenceRequest) (*http.Response, error) {
	path, ok := InferencePath(request.API)
	if !ok {
		return nil, errors.New("unsupported Prism API")
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, " \t\r\n") {
		return nil, errors.New("Circles credential is invalid")
	}
	requestURL := strings.TrimSuffix(c.BaseURL, "/") + path
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(request.Body))
	if err != nil {
		return nil, errors.New("could not create the Prism request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	if request.Provider != "" {
		httpRequest.Header.Set("X-Prism-Provider", request.Provider)
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: InferenceConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   InferenceConnectTimeout,
				ResponseHeaderTimeout: InferenceFirstByteTimeout,
				IdleConnTimeout:       90 * time.Second,
			},
		}
	} else {
		// A caller-provided client may have a legacy total timeout. Inference
		// streams are allowed to run longer, so clear that one setting while
		// preserving the caller's transport, jar, and redirect policy.
		copy := *client
		copy.Timeout = 0
		client = &copy
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func CloseResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}
