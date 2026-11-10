// Copyright 2026 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package outline

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"golang.getoutline.org/sdk/transport"
	"localhost/client/go/configyaml"
	"localhost/client/go/outline/configregistry"
)

type measurementResult struct {
	OK           bool     `json:"ok"`
	LatencyMS    *float64 `json:"latencyMs,omitempty"`
	DownloadMbps *float64 `json:"downloadMbps,omitempty"`
	UploadMbps   *float64 `json:"uploadMbps,omitempty"`
	MeasuredAt   string   `json:"measuredAt"`
	Bytes        int64    `json:"bytes"`
	Error        string   `json:"error,omitempty"`
}

var measurements = struct {
	sync.Mutex
	id        string
	cancel    context.CancelFunc
	cancelled map[string]time.Time
}{cancelled: map[string]time.Time{}}

// CancelServerMeasurement accepts only the matching job. Remember early cancels
// briefly so cancelling during native dispatch cannot start a late speed test.
func CancelServerMeasurement(id string) {
	if !validMeasurementID(id) {
		return
	}
	measurements.Lock()
	defer measurements.Unlock()
	for token, expiry := range measurements.cancelled {
		if time.Now().After(expiry) {
			delete(measurements.cancelled, token)
		}
	}
	if len(measurements.cancelled) < 64 {
		measurements.cancelled[id] = time.Now().Add(time.Minute)
	}
	if measurements.id == id && measurements.cancel != nil {
		measurements.cancel()
	}
}

func validMeasurementID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

// MeasureServer measures a selected proxy transport without starting a VPN or
// reporters. On Apple, invoke inside the active provider so sockets stay outside
// its tunnel. The GUI may invoke directly only while the VPN is disconnected.
// Only public test data is transferred; access keys and raw errors never return.
func MeasureServer(config string, speed bool, id string) string {
	result := measurementResult{}
	finish := func() string {
		result.MeasuredAt = time.Now().UTC().Format(time.RFC3339)
		data, _ := json.Marshal(result)
		return string(data)
	}
	if !validMeasurementID(id) || len(config) > 131072 {
		result.Error = "invalid_request"
		return finish()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	measurements.Lock()
	if expiry, ok := measurements.cancelled[id]; ok && time.Now().Before(expiry) {
		delete(measurements.cancelled, id)
		measurements.Unlock()
		result.Error = "cancelled"
		return finish()
	}
	if measurements.cancel != nil {
		measurements.Unlock()
		result.Error = "busy"
		return finish()
	}
	measurements.id, measurements.cancel = id, cancel
	measurements.Unlock()
	defer func() {
		measurements.Lock()
		measurements.id = ""
		measurements.cancel = nil
		delete(measurements.cancelled, id)
		measurements.Unlock()
	}()
	var provider struct{ Transport configyaml.ConfigNode }
	if yaml.Unmarshal([]byte(config), &provider) != nil {
		result.Error = "invalid_config"
		return finish()
	}
	parser := configregistry.NewDefaultTransportProvider(&transport.TCPDialer{Dialer: net.Dialer{Timeout: 8 * time.Second, KeepAlive: -1}}, &transport.UDPDialer{})
	pair, err := parser.Parse(ctx, provider.Transport)
	if err != nil {
		result.Error = "invalid_config"
		return finish()
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return pair.DialStream(ctx, address)
	}, ForceAttemptHTTP2: true, DisableCompression: true, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 32768}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, url string, n int64) (time.Duration, error) {
		var body io.Reader
		if method == http.MethodPost {
			body = io.LimitReader(&measurementBytes{}, n)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Cache-Control", "no-store")
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("User-Agent", "Outline-Control-Connection-Test/1")
		if method == http.MethodPost {
			req.ContentLength = n
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		start := time.Now()
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return 0, io.ErrUnexpectedEOF
		}
		if method == http.MethodGet {
			start = time.Now()
			count, err := io.Copy(io.Discard, io.LimitReader(response.Body, n+1))
			result.Bytes += count
			if err != nil {
				return 0, err
			}
			if count != n {
				return 0, io.ErrUnexpectedEOF
			}
		} else {
			result.Bytes += n
			count, err := io.Copy(io.Discard, io.LimitReader(response.Body, 65537))
			if err != nil {
				return 0, err
			}
			if count > 65536 {
				return 0, io.ErrUnexpectedEOF
			}
		}
		return time.Since(start), nil
	}
	fail := func(code string) string {
		result.Error = code
		if ctx.Err() != nil {
			if ctx.Err() == context.Canceled {
				result.Error = "cancelled"
			} else {
				result.Error = "timeout"
			}
		}
		return finish()
	}
	samples := []float64{}
	for i := 0; i < 4; i++ {
		start := time.Now()
		_, err := request(http.MethodGet, "https://speed.cloudflare.com/__down?bytes=0", 0)
		if err != nil {
			return fail("unreachable")
		}
		if i > 0 {
			samples = append(samples, float64(time.Since(start))/float64(time.Millisecond))
		}
	}
	sort.Float64s(samples)
	result.LatencyMS = &samples[1]
	if speed {
		for _, n := range []int64{256 << 10, 1 << 20, 4 << 20, 16 << 20} {
			elapsed, err := request(http.MethodGet, "https://speed.cloudflare.com/__down?bytes="+strconv.FormatInt(n, 10), n)
			if err != nil {
				return fail("download_failed")
			}
			rate := float64(n) * 8 / elapsed.Seconds() / 1e6
			result.DownloadMbps = &rate
			if elapsed >= 1500*time.Millisecond {
				break
			}
		}
		for _, n := range []int64{256 << 10, 1 << 20, 4 << 20, 8 << 20} {
			elapsed, err := request(http.MethodPost, "https://speed.cloudflare.com/__up", n)
			if err != nil {
				return fail("upload_failed")
			}
			rate := float64(n) * 8 / elapsed.Seconds() / 1e6
			result.UploadMbps = &rate
			if elapsed >= 1500*time.Millisecond {
				break
			}
		}
	}
	result.OK = true
	return finish()
}

// Reuse a small public payload; no user files or application data are uploaded.
type measurementBytes struct{}

func (*measurementBytes) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Keep upload memory bounded independently of the transfer size.
var _ io.Reader = (*measurementBytes)(nil)
