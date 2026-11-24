package chain

import (
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOwnedHTTPBindingDeploymentReceiptHasExplicitLifetime(t *testing.T) {
	var mu sync.Mutex
	methods := map[string]int{}
	chain := "0x61"
	code := "0x6000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage
			Method string
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		methods[req.Method]++
		var result any
		switch req.Method {
		case "eth_chainId":
			result = chain
		case "eth_getCode":
			result = code
		case "eth_call":
			result = "0x0000000000000000000000000000000000000000000000000000000000000001"
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	factory, ok := any(httpBindingTestFactory{}).(interface {
		Open(context.Context, string) (protocolrpc.ChainProvider, error)
	})
	if !ok {
		t.Fatal("owned HTTP lifetime binding unavailable")
	}
	provider, err := factory.Open(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := provider.(interface {
		Close()
		Invalidate()
	})
	defer lifecycle.Close()
	p, err := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: crypto.Keccak256Hash([]byte{0x60, 0}), FromBlock: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = p.ValidateDeployment(context.Background(), provider); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	n := methods["eth_getCode"]
	ids := methods["eth_chainId"]
	mu.Unlock()
	if n != 1 || ids != 2 {
		t.Fatalf("immutable reads=%d identity fences=%d", n, ids)
	}
	lifecycle.Invalidate()
	if err = p.ValidateDeployment(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n = methods["eth_getCode"]
	chain = "0x62"
	mu.Unlock()
	if n != 2 {
		t.Fatalf("invalidation reused receipt: %d", n)
	}
	if err = p.ValidateDeployment(context.Background(), provider); err == nil {
		t.Fatal("wrong network accepted")
	}
	mu.Lock()
	chain = "0x61"
	code = "0x6001"
	mu.Unlock()
	if err = p.ValidateDeployment(context.Background(), provider); err == nil {
		t.Fatal("network mismatch retained immutable receipt")
	}
	lifecycle.Close()
	if err = p.ValidateDeployment(context.Background(), provider); err == nil {
		t.Fatal("disposed binding accepted")
	}
}

type httpBindingTestFactory struct{}

func (httpBindingTestFactory) Open(ctx context.Context, endpoint string) (protocolrpc.ChainProvider, error) {
	return NewHTTPBinding(ctx, endpoint)
}

func TestOwnedHTTPBindingRejectsNonceFromRetiredEpoch(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
		}
		json.NewDecoder(r.Body).Decode(&req)
		result := "0x61"
		if req.Method == "eth_call" {
			close(entered)
			<-release
			result = "0x0000000000000000000000000000000000000000000000000000000000000001"
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	b, err := NewHTTPBinding(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, _ := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: common.HexToHash("0x1"), FromBlock: 1}})
	result := make(chan error, 1)
	go func() {
		_, e := p.ReadAuthorizationNonce(context.Background(), b, common.HexToAddress("0x22"))
		result <- e
	}()
	<-entered
	b.Invalidate()
	close(release)
	if err = <-result; err == nil {
		t.Fatal("nonce from retired binding epoch accepted")
	}
}
func TestOwnedHTTPBindingInvalidationWhileWaitingReceiptLockFails(t *testing.T) {
	seen := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
		}
		json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "eth_chainId":
			seen <- struct{}{}
			result = "0x61"
		case "eth_getCode":
			result = "0x01"
		default:
			result = "0x0000000000000000000000000000000000000000000000000000000000000001"
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	b, err := NewHTTPBinding(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, _ := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: crypto.Keccak256Hash([]byte{1}), FromBlock: 1}})
	if err = p.ValidateDeployment(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	<-seen
	p.receiptMu.Lock()
	result := make(chan error, 1)
	go func() { result <- p.ValidateDeployment(context.Background(), b) }()
	<-seen
	b.Invalidate()
	p.receiptMu.Unlock()
	if err = <-result; err == nil {
		t.Fatal("receipt from retired binding epoch accepted")
	}
}

func TestOwnedHTTPBindingInvalidationDuringNetworkReadFails(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var codeCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
		}
		json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "eth_chainId":
			close(entered)
			<-release
			result = "0x61"
		case "eth_getCode":
			codeCalls.Add(1)
			result = "0x01"
		default:
			result = "0x0000000000000000000000000000000000000000000000000000000000000001"
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	b, err := NewHTTPBinding(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, _ := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: crypto.Keccak256Hash([]byte{1}), FromBlock: 1}})
	result := make(chan error, 1)
	go func() { result <- p.ValidateDeployment(context.Background(), b) }()
	<-entered
	b.Invalidate()
	close(release)
	if err = <-result; err == nil {
		t.Fatal("retired network read promoted receipt")
	}
	if codeCalls.Load() != 0 {
		t.Fatal("retired network read proceeded to deployment inspection")
	}
}

type customDeploymentProvider struct {
	protocolrpc.ChainProvider
	codeCalls int
}

func (p *customDeploymentProvider) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(97), nil
}
func (p *customDeploymentProvider) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	p.codeCalls++
	return []byte{1}, nil
}
func (p *customDeploymentProvider) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return big.NewInt(1).FillBytes(make([]byte, 32)), nil
}
func TestCustomProviderNeverAcquiresImmutableReceipt(t *testing.T) {
	p, _ := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: crypto.Keccak256Hash([]byte{1}), FromBlock: 1}})
	provider := &customDeploymentProvider{}
	for i := 0; i < 2; i++ {
		if err := p.ValidateDeployment(context.Background(), provider); err != nil {
			t.Fatal(err)
		}
	}
	if provider.codeCalls != 2 {
		t.Fatalf("custom provider skipped immutable policy: code calls=%d", provider.codeCalls)
	}
}

func TestOwnedHTTPBindingPreservesNativeFinalityClassifications(t *testing.T) {
	for _, target := range []string{"safe", "finalized"} {
		for _, tc := range []struct {
			mode string
			want protocolrpc.FinalityStatus
		}{
			{"hash", protocolrpc.FinalityInconsistent}, {"header", protocolrpc.FinalityInconsistent}, {"null", protocolrpc.FinalityUnavailable}, {"unsupported", protocolrpc.FinalityUnsupported}, {"valid", protocolrpc.FinalityAvailable},
		} {
			t.Run(target+"/"+tc.mode, func(t *testing.T) {
				header := &types.Header{Number: big.NewInt(10), Time: 100, Difficulty: big.NewInt(0)}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						ID     uint64
						Params []json.RawMessage
					}
					json.NewDecoder(r.Body).Decode(&req)
					var tag string
					json.Unmarshal(req.Params[0], &tag)
					raw, _ := json.Marshal(header)
					var wire map[string]any
					json.Unmarshal(raw, &wire)
					var result any = wire
					if tag == target {
						switch tc.mode {
						case "hash":
							wire["hash"] = common.HexToHash("0x1234")
						case "header":
							result = map[string]any{"number": "0xa"}
						case "null":
							result = nil
						case "unsupported":
							json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "tag unsupported"}})
							return
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				}))
				defer server.Close()
				binding, err := NewHTTPBinding(context.Background(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				defer binding.Close()
				finality := protocolrpc.ReadChainFinality(context.Background(), binding)
				got := finality.Safe.Status
				if target == "finalized" {
					got = finality.Finalized.Status
				}
				if got != tc.want {
					t.Fatalf("native finality %s/%s=%s want=%s", target, tc.mode, got, tc.want)
				}
			})
		}
	}
}
