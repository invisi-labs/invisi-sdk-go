package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type countedServiceTransport struct {
	http.RoundTripper
	requests atomic.Int64
}

func (t *countedServiceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return t.RoundTripper.RoundTrip(r)
}

// Restoring the old nil Transport default must break the default cases: they
// would use the caller-controlled global policy and lose reusable evidence.
func TestDefaultServiceTransportKeepsCallerPolicySeparate(t *testing.T) {
	d := ProtocolDeployment{ChainID: "31337", Protocol: ContractDeployment{Address: common.HexToAddress("0xac"), RuntimeCodeHash: common.HexToHash("0xbb")}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct{ ID uint64 }
		_ = json.NewDecoder(r.Body).Decode(&q)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": d}})
	}))
	defer server.Close()
	base := new(http.Transport)
	defer base.CloseIdleConnections()
	global := &countedServiceTransport{RoundTripper: base}
	previous := http.DefaultTransport
	http.DefaultTransport = global
	defer func() { http.DefaultTransport = previous }()
	custom := &countedServiceTransport{RoundTripper: base}
	for _, tc := range []struct {
		name                     string
		options                  []ClientOptions
		reusable                 bool
		globalCalls, customCalls int64
	}{
		{"no options", nil, true, 0, 0},
		{"zero options", []ClientOptions{{}}, true, 0, 0},
		{"explicit native", []ClientOptions{{UseNativeHTTPTransport: true}}, true, 0, 0},
		{"custom transport", []ClientOptions{{HTTPClient: &http.Client{Transport: custom}}}, false, 0, 1},
		{"custom nil transport", []ClientOptions{{HTTPClient: &http.Client{}}}, false, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global.requests.Store(0)
			custom.requests.Store(0)
			c, err := NewProtocolRpcClient(server.URL+"/v1", tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := c.VerifyDeployment(context.Background(), d)
			if err != nil {
				t.Fatal(err)
			}
			if !observation.Current() || observation.Reusable() != tc.reusable || global.requests.Load() != tc.globalCalls || custom.requests.Load() != tc.customCalls {
				t.Fatalf("reusable=%v global=%d custom=%d", observation.Reusable(), global.requests.Load(), custom.requests.Load())
			}
		})
	}
}

func TestServiceDeploymentObservationRetiredByNewerRequest(t *testing.T) {
	ctx := context.Background()
	d := ProtocolDeployment{ChainID: "31337", Protocol: ContractDeployment{Address: common.HexToAddress("0xac"), RuntimeCodeHash: common.HexToHash("0xbb")}}
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant") != "original" {
			t.Error("changed caller-owned headers reached service")
		}
		var q struct{ ID uint64 }
		_ = json.NewDecoder(r.Body).Decode(&q)
		switch requests.Add(1) {
		case 2:
			close(started)
			<-release
		case 3:
			http.Error(w, "failed refresh", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": d}})
	}))
	defer server.Close()
	opts := ClientOptions{UseNativeHTTPTransport: true, Deployment: &d, Headers: http.Header{"X-Tenant": []string{"original"}}}
	c, err := NewProtocolRpcClient(server.URL+"/v1", opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Headers.Set("X-Tenant", "changed")
	initial, err := c.VerifyDeployment(ctx, d)
	if err != nil || !initial.Reusable() {
		t.Fatalf("initial service proof: %v", err)
	}
	clone := c.WithProvider(testProvider{})
	done := make(chan bool, 1)
	go func() { old, err := clone.VerifyDeployment(ctx, d); done <- err == nil && !old.Current() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("slower service request did not start")
	}
	if initial.Current() {
		t.Error("older proof survived start of newer lookup")
	}
	if _, err = c.GetDeployment(ctx, 31337); err == nil {
		t.Error("newer failed lookup returned success")
	}
	close(release)
	if !<-done || initial.Current() {
		t.Fatal("a matching overlapping response must complete without restoring retired observations")
	}
	next, err := c.VerifyDeployment(ctx, d)
	if err != nil || !next.Reusable() || requests.Load() != 4 {
		t.Fatalf("fresh retry: observation=%v calls=%d err=%v", next, requests.Load(), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = clone.GetDeployment(cancelled, 31337); err == nil || next.Current() {
		t.Fatal("cancelled refresh retained old service observation")
	}
	opts.HTTPClient = server.Client()
	if _, err = NewProtocolRpcClient(server.URL+"/v1", opts); err == nil {
		t.Fatal("native transport accepted a caller HTTP policy")
	}
}
