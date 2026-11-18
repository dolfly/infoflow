package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

type Client struct {
	baseURL        string
	requestTimeout time.Duration
	httpClient     *http.Client
	accessChannel  string
}

// New creates an HTTP transport client. accessChannel is the value sent in the
// X-OP-ACCESS-CHANNEL header on every request (including the app token
// request); an empty value disables the header.
func New(baseURL string, connectTimeout, requestTimeout time.Duration, injected *http.Client, accessChannel string) *Client {
	httpClient := injected
	if httpClient == nil {
		baseTransport, ok := http.DefaultTransport.(*http.Transport)
		if ok {
			baseTransport = baseTransport.Clone()
		} else {
			// Match net/http's documented default transport policy even when an
			// application has replaced the mutable http.DefaultTransport value.
			baseTransport = &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
			}
		}
		baseTransport.DialContext = (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext
		httpClient = &http.Client{Transport: baseTransport}
	}
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		requestTimeout: requestTimeout,
		httpClient:     httpClient,
		accessChannel:  accessChannel,
	}
}

// PostJSON performs one JSON POST. A non-empty token is formatted using the
// platform's exact Bearer- scheme. This method deliberately has no retry loop.
func (c *Client) PostJSON(ctx context.Context, route string, payload any, token string) ([]byte, error) {
	return c.PostJSONWithHeaders(ctx, route, payload, token, nil)
}

// PutJSON performs one authenticated JSON PUT without retries.
func (c *Client) PutJSON(ctx context.Context, route string, payload any, token string) ([]byte, error) {
	return c.PutJSONWithHeaders(ctx, route, payload, token, nil)
}

func (c *Client) PutJSONWithHeaders(ctx context.Context, route string, payload any, token string, headers http.Header) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, sdkerrors.NewConfig("request", "could not encode JSON")
	}
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPut, c.baseURL+"/"+strings.TrimLeft(route, "/"), bytes.NewReader(body))
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer-"+token)
	}
	return c.do(req)
}

func (c *Client) DeleteWithHeaders(ctx context.Context, route string, query url.Values, token string, headers http.Header) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	u := c.baseURL + "/" + strings.TrimLeft(route, "/")
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(requestContext, http.MethodDelete, u, nil)
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	for key, values := range headers {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer-"+token)
	}
	return c.do(req)
}

// Get performs one authenticated GET request. It deliberately has no retry
// loop because callers decide whether a read is safe to repeat.
func (c *Client) Get(ctx context.Context, route string, query url.Values, token string) ([]byte, error) {
	return c.GetWithHeaders(ctx, route, query, token, nil)
}

// GetWithHeaders performs one GET with route-scoped headers. It is used by
// user APIs whose identity is carried in a token header rather than the
// application Authorization header.
func (c *Client) GetWithHeaders(ctx context.Context, route string, query url.Values, token string, headers http.Header) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	u := c.baseURL + "/" + strings.TrimLeft(route, "/")
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, u, nil)
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer-"+token)
	}
	for key, values := range headers {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return c.do(req)
}

// PostForm posts application/x-www-form-urlencoded data for APIs that are not
// JSON endpoints (currently voice transcription).
func (c *Client) PostForm(ctx context.Context, route string, form url.Values, token string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.baseURL+"/"+strings.TrimLeft(route, "/"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer-"+token)
	}
	return c.do(req)
}

// PostMultipart posts a pre-encoded multipart body with the exact
// Content-Type header produced by the caller's multipart writer. It is used by
// the BOS file upload route where the SDK must control the boundary.
func (c *Client) PostMultipart(ctx context.Context, route string, contentType string, body []byte, token string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.baseURL+"/"+strings.TrimLeft(route, "/"), bytes.NewReader(body))
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer-"+token)
	}
	return c.do(req)
}

// GetAbsoluteURLWithIdentity performs an authenticated byte fetch from an
// absolute URL outside the SDK base host. The identity header carries the app
// access token for the presigned file download link. It follows redirects and
// returns the response bytes, headers, and status. When maxBytes is positive,
// the response body is capped and an error is returned if the download
// exceeds the limit.
func (c *Client) GetAbsoluteURLWithIdentity(ctx context.Context, target string, identity string, maxBytes int64) ([]byte, http.Header, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, sdkerrors.NewHTTP(0, nil, err)
	}
	if identity != "" {
		req.Header.Set("x-openapi-gateway-identity", identity)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, sdkerrors.NewHTTP(0, nil, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, response.Header.Clone(), sdkerrors.NewHTTP(response.StatusCode, nil, nil)
	}
	var body []byte
	if maxBytes > 0 {
		limited := io.LimitReader(response.Body, maxBytes+1)
		body, err = io.ReadAll(limited)
		if err != nil {
			return nil, response.Header.Clone(), sdkerrors.NewHTTP(response.StatusCode, nil, err)
		}
		if int64(len(body)) > maxBytes {
			return nil, response.Header.Clone(), sdkerrors.NewConfig("file download", "file exceeds maxBytes")
		}
	} else {
		body, err = io.ReadAll(response.Body)
		if err != nil {
			return nil, response.Header.Clone(), sdkerrors.NewHTTP(response.StatusCode, nil, err)
		}
	}
	return append([]byte(nil), body...), response.Header.Clone(), nil
}

func (c *Client) do(request *http.Request) ([]byte, error) {
	c.applyCommonHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, sdkerrors.NewHTTP(response.StatusCode, nil, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, sdkerrors.NewHTTP(response.StatusCode, body, nil)
	}
	return append([]byte(nil), body...), nil
}

// PostJSONWithHeaders is the route-specific extension used by runtime
// registration. Existing L1 callers continue through PostJSON with no added
// headers or wire changes.
func (c *Client) PostJSONWithHeaders(
	ctx context.Context,
	route string,
	payload any,
	token string,
	headers http.Header,
) ([]byte, error) {
	return c.postJSONWithHeaders(ctx, route, payload, token, headers, true)
}

// PostSensitiveJSONWithHeaders performs a route-specific POST without
// following redirects. It is reserved for requests whose JSON body contains
// credentials that must never be replayed to a redirect target.
func (c *Client) PostSensitiveJSONWithHeaders(
	ctx context.Context,
	route string,
	payload any,
	token string,
	headers http.Header,
) ([]byte, error) {
	return c.postJSONWithHeaders(ctx, route, payload, token, headers, false)
}

func (c *Client) postJSONWithHeaders(
	ctx context.Context,
	route string,
	payload any,
	token string,
	headers http.Header,
	followRedirects bool,
) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, sdkerrors.NewConfig("request", "could not encode JSON")
	}

	requestContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		c.baseURL+"/"+strings.TrimLeft(route, "/"),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer-"+token)
	}
	c.applyCommonHeaders(request)

	httpClient := c.httpClient
	if !followRedirects {
		clientCopy := *httpClient
		clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		httpClient = &clientCopy
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, sdkerrors.NewHTTP(0, nil, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, sdkerrors.NewHTTP(response.StatusCode, nil, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, sdkerrors.NewHTTP(response.StatusCode, responseBody, nil)
	}
	return append([]byte(nil), responseBody...), nil
}

// applyCommonHeaders stamps SDK-wide headers onto a request. Currently this is
// the X-OP-ACCESS-CHANNEL access channel marker; it is applied to every
// request (including the app token request) regardless of route.
func (c *Client) applyCommonHeaders(request *http.Request) {
	if c.accessChannel != "" {
		request.Header.Set("X-OP-ACCESS-CHANNEL", c.accessChannel)
	}
}
