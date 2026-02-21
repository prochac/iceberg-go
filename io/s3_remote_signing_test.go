// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package io

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSigningRoundTripper(t *testing.T) {
	t.Parallel()

	t.Run("signs GET request", func(t *testing.T) {
		t.Parallel()

		var receivedSignReq s3SignRequest
		signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

			err := json.NewDecoder(r.Body).Decode(&receivedSignReq)
			require.NoError(t, err)

			json.NewEncoder(w).Encode(s3SignResponse{
				URI: receivedSignReq.URI,
				Headers: map[string][]string{
					"Authorization":        {"AWS4-HMAC-SHA256 Credential=..."},
					"X-Amz-Date":           {"20240101T000000Z"},
					"X-Amz-Content-Sha256": {emptyStringSHA256},
				},
			})
		}))
		defer signerServer.Close()

		s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "AWS4-HMAC-SHA256 Credential=...", r.Header.Get("Authorization"))
			assert.Equal(t, "20240101T000000Z", r.Header.Get("X-Amz-Date"))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("s3-response-body"))
		}))
		defer s3Server.Close()

		rt := &signingRoundTripper{
			base:      http.DefaultTransport,
			signerURI: signerServer.URL,
			token:     "test-token",
			region:    "us-east-1",
		}

		req, err := http.NewRequest(http.MethodGet, s3Server.URL+"/bucket/key", nil)
		require.NoError(t, err)

		resp, err := rt.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "s3-response-body", string(body))

		// Verify the sign request was correct.
		assert.Equal(t, "GET", receivedSignReq.Method)
		assert.Equal(t, "us-east-1", receivedSignReq.Region)
		assert.Contains(t, receivedSignReq.URI, "/bucket/key")
	})

	t.Run("signs PUT request with body", func(t *testing.T) {
		t.Parallel()

		var receivedSignReq s3SignRequest
		signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := json.NewDecoder(r.Body).Decode(&receivedSignReq)
			require.NoError(t, err)

			json.NewEncoder(w).Encode(s3SignResponse{
				URI: receivedSignReq.URI,
				Headers: map[string][]string{
					"Authorization": {"AWS4-HMAC-SHA256 Credential=signed"},
				},
			})
		}))
		defer signerServer.Close()

		var receivedBody []byte
		s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		defer s3Server.Close()

		rt := &signingRoundTripper{
			base:      http.DefaultTransport,
			signerURI: signerServer.URL,
			token:     "test-token",
			region:    "us-west-2",
		}

		putBody := []byte("file-content")
		req, err := http.NewRequest(http.MethodPut, s3Server.URL+"/bucket/key", bytes.NewReader(putBody))
		require.NoError(t, err)
		req.ContentLength = int64(len(putBody))

		resp, err := rt.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "PUT", receivedSignReq.Method)
		assert.Equal(t, "us-west-2", receivedSignReq.Region)
		// The body should still be sent to S3 after signing.
		assert.Equal(t, "file-content", string(receivedBody))
		// Content hash should be set in the headers sent to the signer.
		assert.NotEmpty(t, receivedSignReq.Headers["X-Amz-Content-Sha256"])
	})

	t.Run("signer error returns error", func(t *testing.T) {
		t.Parallel()

		signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer signerServer.Close()

		rt := &signingRoundTripper{
			base:      http.DefaultTransport,
			signerURI: signerServer.URL,
			token:     "expired-token",
			region:    "us-east-1",
		}

		req, err := http.NewRequest(http.MethodGet, "http://example.com/bucket/key", nil)
		require.NoError(t, err)

		_, err = rt.RoundTrip(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 403")
	})

	t.Run("works without bearer token", func(t *testing.T) {
		t.Parallel()

		signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.Header.Get("Authorization"))

			json.NewEncoder(w).Encode(s3SignResponse{
				Headers: map[string][]string{
					"Authorization": {"AWS4-HMAC-SHA256 Credential=signed"},
				},
			})
		}))
		defer signerServer.Close()

		s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer s3Server.Close()

		rt := &signingRoundTripper{
			base:      http.DefaultTransport,
			signerURI: signerServer.URL,
			token:     "",
			region:    "us-east-1",
		}

		req, err := http.NewRequest(http.MethodGet, s3Server.URL+"/bucket/key", nil)
		require.NoError(t, err)

		resp, err := rt.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("updates URI from signer response", func(t *testing.T) {
		t.Parallel()

		signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var signReq s3SignRequest
			json.NewDecoder(r.Body).Decode(&signReq)

			json.NewEncoder(w).Encode(s3SignResponse{
				URI: signReq.URI + "?X-Amz-Signature=abc123",
				Headers: map[string][]string{
					"Authorization": {"AWS4-HMAC-SHA256 Credential=signed"},
				},
			})
		}))
		defer signerServer.Close()

		var receivedURL string
		s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedURL = r.URL.String()
			w.WriteHeader(http.StatusOK)
		}))
		defer s3Server.Close()

		rt := &signingRoundTripper{
			base:      http.DefaultTransport,
			signerURI: signerServer.URL,
			token:     "test-token",
			region:    "us-east-1",
		}

		req, err := http.NewRequest(http.MethodGet, s3Server.URL+"/bucket/key", nil)
		require.NoError(t, err)

		resp, err := rt.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Contains(t, receivedURL, "X-Amz-Signature=abc123")
	})
}

func TestSignerEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"base URI", "https://catalog.example.com", "https://catalog.example.com/v1/aws/s3/sign"},
		{"base URI with trailing slash", "https://catalog.example.com/", "https://catalog.example.com/v1/aws/s3/sign"},
		{"base URI with path", "https://catalog.example.com/api", "https://catalog.example.com/api/v1/aws/s3/sign"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, signerEndpoint(tt.input))
		})
	}
}

func TestIsRemoteSigningEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		props    map[string]string
		expected bool
	}{
		{"enabled", map[string]string{S3RemoteSigningEnabled: "true"}, true},
		{"disabled", map[string]string{S3RemoteSigningEnabled: "false"}, false},
		{"not set", map[string]string{}, false},
		{"invalid value", map[string]string{S3RemoteSigningEnabled: "invalid"}, false},
		{"True uppercase", map[string]string{S3RemoteSigningEnabled: "True"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, isRemoteSigningEnabled(tt.props))
		})
	}
}
