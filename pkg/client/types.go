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
	"encoding/json"
	"fmt"
)

// ErrorType is the type of the API error.
type ErrorType string

const (
	ErrBadData     ErrorType = "bad_data"
	ErrTimeout     ErrorType = "timeout"
	ErrCanceled    ErrorType = "canceled"
	ErrExec        ErrorType = "execution"
	ErrBadResponse ErrorType = "bad_response"
)

// Error is an error returned by the API.
type Error struct {
	Type ErrorType
	ErrorMsg  string
	Query string
	StatusCode int
}

func (e *Error) Error() string {
	base := fmt.Sprintf("%s: %s", e.Type, e.ErrorMsg)
	if e.Query != "" {
		return fmt.Sprintf("%s for status code: %d, query: %s", base, e.StatusCode, e.Query)
	}
	return base
}

// ResponseStatus is the type of response from the API: succeeded or error.
type ResponseStatus string

const (
	ResponseSucceeded ResponseStatus = "succeeded"
	ResponseError     ResponseStatus = "error"
)

// APIResponse represents the raw response returned by the API.
type APIResponse struct {
	// Status indicates whether this request was successful or whether it errored out.
	Status ResponseStatus `json:"status"`
	// Data contains the raw data response for this request.
	Data json.RawMessage `json:"data"`

	// ErrorType is the type of error, if this is an error response.
	ErrorType ErrorType `json:"errorType"`
	// Error is the error message, if this is an error response.
	Error string `json:"error"`

	// StatusCode is the status code returned by the server
	StatusCode int `json:"statusCode"`
}
