// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package prometheus provides bindings to the Prometheus HTTP API:
// http://prometheus.io/docs/querying/api/
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"
)

// APIClient is a raw client to the Prometheus Query API.
// It knows how to appropriately deal with generic Prometheus API
// responses, but does not know the specifics of different endpoints.
// You can use this to call query endpoints not represented in Client.
type GenericAPIClient interface {
	// Do makes a request to the Prometheus HTTP API against a particular endpoint.  Query
	// parameters should be in `query`, not `endpoint`.  An error will be returned on HTTP
	// status errors or errors making or unmarshalling the request, as well as when the
	// response has a Status of ResponseError.
	Do(ctx context.Context, verb, endpoint string, query url.Values) (APIResponse, *Error)
}

// httpAPIClient is a GenericAPIClient implemented in terms of an underlying http.Client.
type httpAPIClient struct {
	client  *http.Client
	baseURL *url.URL
	headers http.Header
}

func (c *httpAPIClient) Do(ctx context.Context, verb, endpoint string, query url.Values) (APIResponse, *Error) {
	u := *c.baseURL
	u.Path = path.Join(c.baseURL.Path, endpoint)
	var reqBody io.Reader
	if verb == http.MethodGet {
		u.RawQuery = query.Encode()
	} else if verb == http.MethodPost {
		reqBody = strings.NewReader(query.Encode())
	}
	queryStr := query.Encode()
	if unescapedQueryStr, err := url.QueryUnescape(queryStr); err == nil {
		queryStr = unescapedQueryStr
	} else {
		klog.Errorf("Error %v unescaping query %s", err, queryStr)
	}

	req, err := http.NewRequestWithContext(ctx, verb, u.String(), reqBody)
	if err != nil {
		return APIResponse{}, &Error{
			Type: ErrExec,
			ErrorMsg:  fmt.Sprintf("error constructing HTTP request to Prometheus: %v", err),
			Query: queryStr,
			StatusCode: 0, // No status code since no request was sent
		}

	}
	for key, values := range c.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if verb == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.client.Do(req)
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return APIResponse{}, &Error{
		  	Type: ErrExec,
		  	ErrorMsg:  err.Error(),
			StatusCode: resp.StatusCode,
			Query: queryStr,
		}
	}

	if klog.V(6).Enabled() {
		klog.Infof("%s %s %s", verb, u.String(), resp.Status)
	}

	code := resp.StatusCode

	// codes that aren't 2xx, 400, 422, or 503 won't return JSON objects
	if code/100 != 2 && code != 400 && code != 422 && code != 503 {
		return APIResponse{}, &Error{
		  	Type: ErrBadResponse,
		  	ErrorMsg: "No JSON object in response with this error code.",
			StatusCode: code,
			Query: queryStr,
		}
	}

	var body io.Reader = resp.Body
	data, err := io.ReadAll(body)
	if err != nil {
		return APIResponse{}, &Error{
			Type: ErrBadResponse,
			ErrorMsg:  fmt.Sprintf("unable to read response body %s with error %v", string(data), err),
			StatusCode: code,
			Query: queryStr,
		} 
	}
	body = bytes.NewReader(data)

	var res APIResponse
	if err = json.NewDecoder(body).Decode(&res); err != nil {
		return APIResponse{}, &Error{
		  	Type: ErrBadResponse,
		  	ErrorMsg:  err.Error(),
			StatusCode: code,
			Query: queryStr,
		}
	}

	if res.Status == ResponseError {
		return res, &Error{
			Type: res.ErrorType,
			ErrorMsg:  res.Error,
			StatusCode: code,
			Query: queryStr,
		}
	}

	return res, nil
}

// NewGenericAPIClient builds a new generic Prometheus API client for the given base URL and HTTP Client.
func NewGenericAPIClient(client *http.Client, baseURL *url.URL, headers http.Header) GenericAPIClient {
	return &httpAPIClient{
		client:  client,
		baseURL: baseURL,
		headers: headers,
	}
}

const (
	queryURL      = "/api/v1/query"
	queryRangeURL = "/api/v1/query_range"
	seriesURL     = "/api/v1/series"
)

// queryClient is a Client that connects to the Prometheus HTTP API.
type queryClient struct {
	api  GenericAPIClient
	verb string
}

// NewClientForAPI creates a Client for the given generic Prometheus API client.
func NewClientForAPI(client GenericAPIClient, verb string) Client {
	return &queryClient{
		api:  client,
		verb: verb,
	}
}

// NewClient creates a Client for the given HTTP client and base URL (the location of the Prometheus server).
func NewClient(client *http.Client, baseURL *url.URL, headers http.Header, verb string) Client {
	genericClient := NewGenericAPIClient(client, baseURL, headers)
	return NewClientForAPI(genericClient, verb)
}

func (h *queryClient) Series(ctx context.Context, interval model.Interval, selectors ...Selector) ([]Series, *Error) {
	vals := url.Values{}
	if interval.Start != 0 {
		vals.Set("start", interval.Start.String())
	}
	if interval.End != 0 {
		vals.Set("end", interval.End.String())
	}

	for _, selector := range selectors {
		vals.Add("match[]", string(selector))
	}

	res, err := h.api.Do(ctx, h.verb, seriesURL, vals)
	if err != nil {
		return nil, err
	}

	queryStr := vals.Encode()
	if unescapedQueryStr, err := url.QueryUnescape(queryStr); err == nil {
		queryStr = unescapedQueryStr
	} else {
		klog.Errorf("Error %v unescaping query %s", err, queryStr)
	}

	var seriesRes []Series
	if err := json.Unmarshal(res.Data, &seriesRes); err != nil {
		return nil, &Error{
			Type:       ErrBadData,
			ErrorMsg:   fmt.Sprintf("failed to unmarshal JSON response: %v", err),
			StatusCode: http.StatusOK, // Use http.StatusOK instead of hardcoded 200. Since err was not returned from api.Do(), api call/response was successful
			Query:      queryStr,
		}
	}
	return seriesRes, nil
}

func (h *queryClient) Query(ctx context.Context, t model.Time, query Selector) (QueryResult, *Error) {
	vals := url.Values{}
	vals.Set("query", string(query))
	if t != 0 {
		vals.Set("time", t.String())
	}
	if timeout, hasTimeout := timeoutFromContext(ctx); hasTimeout {
		vals.Set("timeout", model.Duration(timeout).String())
	}

	res, err := h.api.Do(ctx, h.verb, queryURL, vals)
	if err != nil {
		return QueryResult{}, err
	}

	var queryRes QueryResult
	if err := json.Unmarshal(res.Data, &queryRes); err != nil {
		return queryRes, &Error{
			Type:        ErrBadData,
			ErrorMsg:    fmt.Sprintf("failed to unmarshal JSON response: %v", err),
			StatusCode:  http.StatusOK, // Use http.StatusOK instead of hardcoded 200. Since err was not returned from query(), api call/response was successful
			Query:       string(query),
		}
	}
	
	return queryRes, nil
}

func (h *queryClient) QueryRange(ctx context.Context, r Range, query Selector) (QueryResult, *Error) {
	vals := url.Values{}
	vals.Set("query", string(query))

	if r.Start != 0 {
		vals.Set("start", r.Start.String())
	}
	if r.End != 0 {
		vals.Set("end", r.End.String())
	}
	if r.Step != 0 {
		vals.Set("step", model.Duration(r.Step).String())
	}
	if timeout, hasTimeout := timeoutFromContext(ctx); hasTimeout {
		vals.Set("timeout", model.Duration(timeout).String())
	}

	res, err := h.api.Do(ctx, h.verb, queryRangeURL, vals)
	if err != nil {
		return QueryResult{}, err
	}

	var queryRes QueryResult
	if err := json.Unmarshal(res.Data, &queryRes); err != nil {
		return queryRes, &Error{
			Type:        ErrBadData,
			ErrorMsg:    fmt.Sprintf("failed to unmarshal JSON response: %v", err),
			StatusCode:  http.StatusOK, // Use http.StatusOK instead of hardcoded 200. Since err was not returned from query(), api call/response was successful
			Query:       string(query),
		}
	}
	return queryRes, nil
}

// timeoutFromContext checks the context for a deadline and calculates a "timeout" duration from it,
// when present
func timeoutFromContext(ctx context.Context) (time.Duration, bool) {
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		return time.Since(deadline), true
	}

	return time.Duration(0), false
}
