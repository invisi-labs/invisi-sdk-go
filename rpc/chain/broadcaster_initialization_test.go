package chain

import (
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExplicitBroadcasterInitializationReusesOnlyOwnedBindingEpoch(t *testing.T) {
	var lookups, probes atomic.Int64
	var failRefresh, unsupported, holdNext atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	contract := protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: crypto.Keccak256Hash([]byte{1}), FromBlock: 1}
	d := protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: contract, BroadcasterRegistry: &contract}
	token := common.HexToAddress("0x22")
	signer := common.HexToAddress("0x33")
	recipient := common.HexToAddress("0x44")
	block := protocolrpc.CanonicalBlock{Number: 10, Hash: common.HexToHash("0x10"), Timestamp: 100}
	var endpoint string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "eth_chainId":
			result = "0x61"
		case "eth_getCode":
			result = "0x01"
		case "eth_call":
			result = "0x0000000000000000000000000000000000000000000000000000000000000001"
		case "invisi_getBroadcasters":
			lookups.Add(1)
			if holdNext.Swap(false) {
				close(entered)
				<-release
			}
			if failRefresh.Load() {
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "discovery unavailable"}})
				return
			}
			result = protocolrpc.BroadcasterPage{Block: block, Broadcasters: []protocolrpc.RegisteredBroadcaster{{Operator: common.HexToAddress("0x1"), Endpoint: endpoint, QuoteSigner: signer, FeeRecipient: recipient, Enabled: true, Block: block}}}
		case "broadcaster_getTokenSupport":
			probes.Add(1)
			var input struct{ TokenAddress common.Address }
			if len(req.Params) > 0 {
				json.Unmarshal(req.Params[0], &input)
			}
			result = map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": "97", "protocol": contract.Address.Hex(), "sender": signer.Hex(), "feeRecipient": recipient.Hex(), "submissionHalted": false, "provenance": map[string]any{"enabled": false}, "tokenAddress": input.TokenAddress.Hex(), "supported": !unsupported.Load()}
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	endpoint = server.URL
	nativeTLS := &http.Client{Transport: &http.Transport{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}}
	binding, err := NewHTTPBinding(context.Background(), endpoint, nativeTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	rpc, _ := protocolrpc.NewProtocolRpcClient(endpoint+"/v1", protocolrpc.ClientOptions{HTTPClient: server.Client(), Deployment: &d})
	p, _ := NewProtocol(d)
	p.rpc = rpc
	p.provider = binding
	init, ok := any(p).(interface {
		InitializeBroadcasters(context.Context, BroadcasterOptions) ([]*broadcaster.Client, error)
	})
	if !ok {
		t.Fatal("explicit discovery initialization unavailable")
	}
	opts := BroadcasterOptions{TokenAddress: token, ClientOptions: broadcaster.Options{HTTPClient: server.Client()}}
	first, err := init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	for i := 0; i < 2; i++ {
		next, err := p.GetBroadcasters(context.Background(), opts)
		if err != nil || len(next) != 1 || next[0] != first[0] {
			t.Fatal("initialized client was not reused", err)
		}
	}
	if lookups.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("lookup=%d probes=%d", lookups.Load(), probes.Load())
	}

	trust := broadcaster.Trust{ChainID: big.NewInt(97), Protocol: contract.Address}
	for i := 0; i < 2; i++ {
		if _, err := first[0].ForPreparation(context.Background(), nil, trust); err != nil {
			t.Fatal("initialized capability was not reused", err)
		}
	}

	originalTransport := opts.ClientOptions.HTTPClient.Transport
	var replacementCalls atomic.Int64
	opts.ClientOptions.HTTPClient.Transport = &initializationPolicyTransport{RoundTripper: originalTransport, calls: &replacementCalls}
	if _, err = p.GetBroadcasters(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if replacementCalls.Load() == 0 {
		t.Fatal("same HTTPClient pointer ignored replaced transport policy")
	}
	if _, err := first[0].ForPreparation(context.Background(), nil, trust); err == nil {
		t.Fatal("retained handle accepted changed HTTP policy")
	}
	opts.ClientOptions.HTTPClient.Transport = originalTransport
	if _, err := first[0].ForPreparation(context.Background(), nil, trust); err == nil {
		t.Fatal("retired HTTP policy authority restored")
	}

	opts.ClientOptions.HTTPClient.Transport = uncomparableInitializationPolicy{base: originalTransport, marker: []int{1}}
	if initialized, err := init.InitializeBroadcasters(context.Background(), opts); err != nil || len(initialized) != 1 {
		t.Fatal("uncomparable policy initialization", err)
	}
	policyLookups := lookups.Load()
	if fresh, err := p.GetBroadcasters(context.Background(), opts); err != nil || len(fresh) != 1 {
		t.Fatal("uncomparable policy lookup", err)
	}
	if lookups.Load() != policyLookups+1 {
		t.Fatal("uncomparable transport policy was retained")
	}
	opts.ClientOptions.HTTPClient.Transport = originalTransport

	opts.ClientOptions.HTTPClient.Transport = nestedInitializationPolicy{base: originalTransport, metadata: map[string]string{"mode": "auth"}}
	if initialized, err := init.InitializeBroadcasters(context.Background(), opts); err != nil || len(initialized) != 1 {
		t.Fatal("nested noncomparable policy initialization", err)
	}
	policyLookups = lookups.Load()
	if fresh, err := p.GetBroadcasters(context.Background(), opts); err != nil || len(fresh) != 1 {
		t.Fatal("nested noncomparable policy lookup", err)
	}
	if lookups.Load() != policyLookups+1 {
		t.Fatal("nested noncomparable transport policy was retained")
	}
	opts.ClientOptions.HTTPClient.Transport = originalTransport

	retained, err := init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(retained) != 1 {
		t.Fatal(retained, err)
	}
	failRefresh.Store(true)
	if _, err = init.InitializeBroadcasters(context.Background(), opts); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if _, err := retained[0].ForPreparation(context.Background(), nil, trust); err == nil {
		t.Fatal("failed refresh retained old client authority")
	}
	failRefresh.Store(false)
	before := lookups.Load()
	if _, err = p.GetBroadcasters(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != before+1 {
		t.Fatal("failed explicit refresh retained previous initialized receipt")
	}
	unsupported.Store(true)
	if empty, err := init.InitializeBroadcasters(context.Background(), opts); err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	unsupported.Store(false)
	before = lookups.Load()
	if found, err := p.GetBroadcasters(context.Background(), opts); err != nil || len(found) != 1 {
		t.Fatal(found, err)
	}
	if lookups.Load() != before+1 {
		t.Fatal("empty initialization was retained")
	}
	retained, err = init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(retained) != 1 {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = init.InitializeBroadcasters(cancelled, opts); err == nil {
		t.Fatal("cancelled refresh succeeded")
	}
	if _, err := retained[0].ForPreparation(context.Background(), nil, trust); err == nil {
		t.Fatal("cancelled refresh retained old client authority")
	}
	before = lookups.Load()
	if _, err = p.GetBroadcasters(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != before+1 {
		t.Fatal("cancelled refresh retained previous receipt")
	}
	holdNext.Store(true)
	pendingDone := make(chan []*broadcaster.Client, 1)
	pendingErr := make(chan error, 1)
	go func() {
		clients, e := init.InitializeBroadcasters(context.Background(), opts)
		pendingDone <- clients
		pendingErr <- e
	}()
	<-entered
	if fresh, e := p.GetBroadcasters(context.Background(), opts); e != nil || len(fresh) != 1 {
		t.Fatal(fresh, e)
	}
	close(release)
	pending := <-pendingDone
	if e := <-pendingErr; e != nil || len(pending) != 1 {
		t.Fatal(pending, e)
	}
	if _, e := pending[0].ForPreparation(context.Background(), nil, trust); e != nil {
		t.Fatal("ordinary lookup revoked in-flight initialization", e)
	}
	entered = make(chan struct{})
	release = make(chan struct{})
	holdNext.Store(true)
	oldDone := make(chan error, 1)
	oldClients := make(chan []*broadcaster.Client, 1)
	go func() {
		clients, e := init.InitializeBroadcasters(context.Background(), opts)
		oldClients <- clients
		oldDone <- e
	}()
	<-entered
	newest, err := init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(newest) != 1 {
		t.Fatal(newest, err)
	}
	close(release)
	<-oldDone
	if old := <-oldClients; len(old) == 1 {
		if _, e := old[0].ForPreparation(context.Background(), nil, trust); e == nil {
			t.Fatal("superseded initialization retained client authority")
		}
	}
	current, err := p.GetBroadcasters(context.Background(), opts)
	if err != nil || len(current) != 1 || current[0] != newest[0] {
		t.Fatal("older concurrent initialization replaced latest receipt", err)
	}
	for i := 0; i < 300; i++ {
		bulk := opts
		bulk.TokenAddress = common.BigToAddress(big.NewInt(int64(1000 + i)))
		found, e := init.InitializeBroadcasters(context.Background(), bulk)
		if e != nil || len(found) != 1 {
			t.Fatal("bounded initialization failed", e)
		}
	}
	p.discoveryMu.Lock()
	count := len(p.discovery)
	p.discoveryMu.Unlock()
	if count > 256 {
		t.Fatalf("unbounded initialized discovery map: %d", count)
	}
	retained, err = init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(retained) != 1 {
		t.Fatal(retained, err)
	}
	originalRPC := p.rpc
	p.rpc = nil
	if _, e := retained[0].ForPreparation(context.Background(), nil, trust); e == nil {
		t.Fatal("changed discovery source retained old client authority")
	}
	p.rpc = originalRPC
	if _, e := retained[0].ForPreparation(context.Background(), nil, trust); e == nil {
		t.Fatal("retired discovery source authority restored")
	}
	retained, err = init.InitializeBroadcasters(context.Background(), opts)
	if err != nil || len(retained) != 1 {
		t.Fatal(retained, err)
	}
	before = lookups.Load()
	binding.Invalidate()
	if _, e := retained[0].ForPreparation(context.Background(), nil, trust); e == nil {
		t.Fatal("retired epoch retained old client authority")
	}
	if _, err = p.GetBroadcasters(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != before+1 {
		t.Fatal("invalidated epoch reused initialized discovery")
	}
}

type initializationPolicyTransport struct {
	http.RoundTripper
	calls *atomic.Int64
}

func (t *initializationPolicyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.RoundTripper.RoundTrip(r)
}

type uncomparableInitializationPolicy struct {
	base   http.RoundTripper
	marker []int
}

func (t uncomparableInitializationPolicy) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r)
}

type nestedInitializationPolicy struct {
	base     http.RoundTripper
	metadata any
}

func (t nestedInitializationPolicy) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r)
}

func TestBroadcasterInitializationPolicyTracksDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	implicit, _ := captureBroadcasterHTTPPolicy(nil)
	client := &http.Client{}
	explicit, _ := captureBroadcasterHTTPPolicy(client)
	http.DefaultTransport = &initializationPolicyTransport{RoundTripper: original, calls: &atomic.Int64{}}
	if implicit.matches(nil) || explicit.matches(client) {
		t.Fatal("default transport replacement retained initialized HTTP policy")
	}
}
