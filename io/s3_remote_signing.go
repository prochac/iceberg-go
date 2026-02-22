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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type signingTokenProviderKey struct{}

// SigningTokenProvider is a function that returns a fresh bearer token for
// the S3 remote signing endpoint.
type SigningTokenProvider func() (string, error)

// WithSigningTokenProvider returns a new context that carries the given
// token provider for use by the S3 remote signing transport.
func WithSigningTokenProvider(ctx context.Context, tp SigningTokenProvider) context.Context {
	return context.WithValue(ctx, signingTokenProviderKey{}, tp)
}

// GetSigningTokenProvider retrieves the signing token provider from ctx,
// or nil if none is set.
func GetSigningTokenProvider(ctx context.Context) SigningTokenProvider {
	if v := ctx.Value(signingTokenProviderKey{}); v != nil {
		return v.(SigningTokenProvider)
	}

	return nil
}

// s3SignRequest is the request body for the remote S3 signing endpoint.
type s3SignRequest struct {
	Method  string              `json:"method"`
	Region  string              `json:"region"`
	URI     string              `json:"uri"`
	Headers map[string][]string `json:"headers"`
}

// s3SignResponse is the response from the remote S3 signing endpoint.
type s3SignResponse struct {
	URI     string              `json:"uri"`
	Headers map[string][]string `json:"headers"`
}

// signingRoundTripper is an http.RoundTripper that intercepts S3 HTTP
// requests and delegates signing to a remote REST catalog endpoint
// (POST {signerURI}/v1/aws/s3/sign). This implements the Iceberg REST
// catalog S3 remote signing protocol.
type signingRoundTripper struct {
	base          http.RoundTripper
	signerURI     string                 // full URL to the signing endpoint
	tokenProvider func() (string, error) // bearer token provider for the signing endpoint
	region        string                 // AWS region for the signing request
}

// from https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/signer/v4#Signer.SignHTTP
const emptyStringSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *signingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Ensure x-amz-content-sha256 is set for the signing request.
	if req.Header.Get("x-amz-content-sha256") == "" {
		if req.Body == nil || req.ContentLength == 0 {
			req.Header.Set("x-amz-content-sha256", emptyStringSHA256)
		} else {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read request body for signing: %w", err)
			}
			req.Body.Close()

			h := sha256.Sum256(body)
			req.Header.Set("x-amz-content-sha256", hex.EncodeToString(h[:]))
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
		}
	}

	// Build the signing request with current request details.
	headers := make(map[string][]string, len(req.Header))
	for k, v := range req.Header {
		headers[k] = v
	}

	signReqBody := s3SignRequest{
		Method:  req.Method,
		Region:  s.region,
		URI:     req.URL.String(),
		Headers: headers,
	}

	data, err := json.Marshal(signReqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal S3 sign request: %w", err)
	}

	signReq, err := http.NewRequestWithContext(
		req.Context(), http.MethodPost, s.signerURI, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 sign request: %w", err)
	}
	signReq.Header.Set("Content-Type", "application/json")
	token, err := s.tokenProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get signing token: %w", err)
	}
	if token != "" {
		signReq.Header.Set("Authorization", "Bearer "+token)
	}

	signResp, err := s.base.RoundTrip(signReq)
	if err != nil {
		return nil, fmt.Errorf("S3 sign request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, signResp.Body)
		_ = signResp.Body.Close()
	}()

	if signResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("S3 sign endpoint returned HTTP %d", signResp.StatusCode)
	}

	var signResult s3SignResponse
	if err := json.NewDecoder(signResp.Body).Decode(&signResult); err != nil {
		return nil, fmt.Errorf("failed to decode S3 sign response: %w", err)
	}

	// Apply signed headers to the original request.
	for k, vals := range signResult.Headers {
		req.Header.Del(k)
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	// Update the request URI if the signing endpoint changed it.
	if signResult.URI != "" {
		newURL, err := url.Parse(signResult.URI)
		if err != nil {
			return nil, fmt.Errorf("failed to parse signed URI %q: %w", signResult.URI, err)
		}
		req.URL = newURL
	}

	// Send the actual S3 request with the signed headers.
	return s.base.RoundTrip(req)
}

// signerEndpoint constructs the full signing endpoint URL. If endpoint
// is provided (e.g. from the s3.signer.endpoint property), it is joined
// to signerURI. Otherwise, the default path /v1/aws/s3/sign is appended.
func signerEndpoint(signerURI, endpoint string) string {
	base := strings.TrimRight(signerURI, "/")
	if endpoint != "" {
		return base + "/" + strings.TrimLeft(endpoint, "/")
	}

	return base + "/v1/aws/s3/sign"
}
