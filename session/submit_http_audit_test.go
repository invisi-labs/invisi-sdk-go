package session

// This diagnostic uses the real go-ethereum HTTP client against a localhost
// fixture. No transaction is sent to a chain; signing uses public test key 1.
import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/primitives"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/rpc/contracts"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type submitHTTPAudit struct {
	mu       sync.Mutex
	methods  map[string]int
	requests int
	last     string
	delay    time.Duration
}

func (a *submitHTTPAudit) record(method string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	a.methods[method]++
	a.last = method
}
func (a *submitHTTPAudit) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = 0
	a.last = ""
	a.methods = map[string]int{}
}
func (a *submitHTTPAudit) report(t *testing.T, phase string, started time.Time) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, _ := json.Marshal(a.methods)
	t.Logf("HTTP_AUDIT phase=%s requests=%d elapsed_ms=%d methods=%s", phase, a.requests, time.Since(started).Milliseconds(), raw)
}
func auditRPCEndpoint(t *testing.T, provider *submissionProvider, a *submitHTTPAudit) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		a.record(req.Method)
		time.Sleep(a.delay)
		var result any
		var err error
		switch req.Method {
		case "eth_chainId":
			result = "0x7a69"
		case "eth_getCode":
			result = "0x6000"
		case "eth_getBlockByNumber":
			result = provider.head
		case "eth_call":
			var call struct {
				To    common.Address `json:"to"`
				Input hexutil.Bytes  `json:"input"`
				Data  hexutil.Bytes  `json:"data"`
			}
			err = json.Unmarshal(req.Params[0], &call)
			data := call.Input
			if len(data) == 0 {
				data = call.Data
			}
			var b []byte
			if err == nil {
				b, err = provider.CallContract(r.Context(), ethereum.CallMsg{To: &call.To, Data: data}, provider.head.Number)
			}
			result = hexutil.Encode(b)
		default:
			err = fmt.Errorf("unexpected RPC method %s", req.Method)
		}
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if err != nil {
			delete(out, "result")
			out["error"] = map[string]any{"code": -32000, "message": err.Error()}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	return server.URL
}
func auditEthClient(t *testing.T, provider *submissionProvider, a *submitHTTPAudit) *ethclient.Client {
	client, err := ethclient.Dial(auditRPCEndpoint(t, provider, a))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
func TestSubmitActualHTTPAudit(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			ctx := context.Background()
			s, token, _ := testSession(t, uint64(time.Now().Unix()))
			fixture := &submissionProvider{sessionProvider: s.options.Provider.(sessionProvider)}
			s.options.Provider = fixture
			s.rpc = s.rpc.WithProvider(fixture)
			if cached {
				if err := s.RefreshState(ctx); err != nil {
					t.Fatal(err)
				}
			}
			audit := &submitHTTPAudit{methods: map[string]int{}, delay: 10 * time.Millisecond}
			chain := auditEthClient(t, fixture, audit)
			s.options.Provider = chain
			s.rpc = s.rpc.WithProvider(chain)
			addr, _ := s.wallet.GetAddress()
			op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: addr, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
			if err != nil {
				t.Fatal(err)
			}
			client, _ := testBroadcaster(t, op, []int64{5}, false)
			if err = client.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
				t.Fatal(err)
			}
			audit.reset()
			start := time.Now()
			req, err := op.ExportFundingAuthorization(ctx)
			if err != nil {
				t.Fatal(err)
			}
			audit.report(t, "funding_export", start)
			sig, err := signFixtureRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			audit.reset()
			start = time.Now()
			op, err = op.WithFundingSignature(ctx, sig)
			if err != nil {
				t.Fatal(err)
			}
			audit.report(t, "funding_attach", start)
			// Relay methods are recorded independently via the real TLS HTTP transport.
			relayAudit := &submitHTTPAudit{methods: map[string]int{}}
			idsAtFreeze := -1
			first := true
			s.options.Store = afterFrozenStore{s.options.Store, func() {
				if idsAtFreeze < 0 {
					audit.mu.Lock()
					idsAtFreeze = audit.methods["eth_chainId"]
					audit.mu.Unlock()
				}
			}}
			checkWire := func(method string) {
				if first && method == "broadcaster_submitTransaction" {
					audit.mu.Lock()
					defer audit.mu.Unlock()
					if idsAtFreeze < 0 || audit.methods["eth_chainId"] != idsAtFreeze+1 || audit.last != "eth_chainId" {
						t.Errorf("first relay wire lacked a final identity read after frozen journal: at freeze=%d reads=%v last=%s", idsAtFreeze, audit.methods, audit.last)
					}
				}
			}
			counted, err := broadcaster.NewClient(client.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: &auditRelayTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, audit: relayAudit, beforeWire: checkWire}}})
			if err != nil {
				t.Fatal(err)
			}
			audit.reset()
			start = time.Now()
			_, err = counted.Submit(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			audit.report(t, "first_submit_chain", start)
			first = false
			expected := 6
			if cached {
				expected = 10
			}
			if audit.requests != expected {
				t.Fatalf("first submit chain HTTP requests=%d, want %d", audit.requests, expected)
			}
			relayAudit.report(t, "first_submit_relay", start)
			if relayAudit.requests != 2 || relayAudit.methods["broadcaster_getStatus"] != 1 || relayAudit.methods["broadcaster_submitTransaction"] != 1 {
				t.Fatalf("first relay requests=%v", relayAudit.methods)
			}
			audit.reset()
			relayAudit.reset()
			start = time.Now()
			_, err = counted.Submit(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			audit.report(t, "known_id_retry_chain", start)
			relayAudit.report(t, "known_id_retry_relay", start)
			if audit.requests != 0 || relayAudit.requests != 1 || relayAudit.methods["broadcaster_getTransactionStatus"] != 1 {
				t.Fatal("known-ID retry repeated validation or submission")
			}
			op.plan.Broadcaster.RequestID = ""
			audit.reset()
			relayAudit.reset()
			start = time.Now()
			_, err = counted.Submit(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			audit.report(t, "unknown_id_retry_chain", start)
			relayAudit.report(t, "unknown_id_retry_relay", start)
			if audit.requests != 0 || relayAudit.requests != 1 || relayAudit.methods["broadcaster_submitTransaction"] != 1 {
				t.Fatal("unknown-ID retry repeated validation or requirements")
			}
		})
	}
}

func auditTLSConfig() *tls.Config { return &tls.Config{InsecureSkipVerify: true} } // localhost fixture only
// Count actual RoundTrip calls; parse a copy so the SDK request is untouched.
type auditRelayTransport struct {
	beforeWire func(string)
	base       http.RoundTripper
	audit      *submitHTTPAudit
}

func (tr *auditRelayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req struct {
		Method string `json:"method"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if tr.beforeWire != nil {
		tr.beforeWire(req.Method)
	}
	tr.audit.record(req.Method)
	return tr.base.RoundTrip(r)
}

// TestSubmitLiveReadHTTPAudit measures read-only live RPC and a cached Shield
// preflight with explicitly substituted balance/allowance results. No relay send.
func TestSubmitLiveReadHTTPAudit(t *testing.T) {
	input := os.Getenv("INVISI_SUBMIT_LIVE_INPUT")
	if input == "" {
		t.Skip("set INVISI_SUBMIT_LIVE_INPUT to network benchmark JSON")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Options struct {
			ChainID     uint64   `json:"chainId"`
			URLs        []string `json:"chainRpcUrls"`
			ProtocolRPC string   `json:"protocolRpcUrl"`
		} `json:"options"`
		TokenAddress common.Address `json:"tokenAddress"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Options.URLs) == 0 {
		t.Fatal("no RPC configured")
	}
	audit := &submitHTTPAudit{methods: map[string]int{}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rpcClient, err := gethrpc.DialOptions(ctx, cfg.Options.URLs[0], gethrpc.WithHTTPClient(&http.Client{Transport: &auditRelayTransport{base: auditReadOnlyTransport{base: http.DefaultTransport}, audit: audit}, Timeout: 15 * time.Second}))
	if err != nil {
		t.Fatal("RPC connect failed (endpoint omitted)")
	}
	defer rpcClient.Close()
	client := ethclient.NewClient(rpcClient)
	for round := 0; round < 3; round++ {
		audit.reset()
		start := time.Now()
		for i := 0; i < 2; i++ {
			id, e := client.ChainID(ctx)
			if e != nil {
				t.Fatal("read-only chain ID request failed (endpoint omitted)")
			}
			if !id.IsUint64() || id.Uint64() != cfg.Options.ChainID {
				t.Fatal("chain ID mismatch")
			}
		}
		audit.report(t, fmt.Sprintf("live_duplicate_chain_id_round_%d", round+1), start)
	}
	liveCachedPreflight(t, ctx, client, audit, cfg.Options.ProtocolRPC, cfg.Options.ChainID, cfg.TokenAddress)
}

// This is the final transport before network IO. It permits a small read-only
// allowlist, preventing a relay or chain submission even if the diagnostic changes.
type auditReadOnlyTransport struct{ base http.RoundTripper }

func (tr auditReadOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body == nil {
		return nil, fmt.Errorf("read-only diagnostic requires JSON-RPC body")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req struct {
		Method string `json:"method"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("read-only diagnostic requires JSON-RPC")
	}
	switch req.Method {
	case "eth_chainId", "eth_getCode", "eth_getBlockByNumber", "eth_call", "invisi_getDeployment":
	default:
		return nil, fmt.Errorf("read-only diagnostic blocked RPC method")
	}
	return tr.base.RoundTrip(r)
}

type auditSyntheticFundingProvider struct {
	sdk.Provider
	token         common.Address
	amount        *big.Int
	substitutions int
}

func (p *auditSyntheticFundingProvider) CallContract(ctx context.Context, m ethereum.CallMsg, b *big.Int) ([]byte, error) {
	raw, err := p.Provider.CallContract(ctx, m, b)
	if err != nil {
		return nil, err
	}
	if m.To != nil && *m.To == p.token && len(m.Data) >= 4 && (bytes.Equal(m.Data[:4], crypto.Keccak256([]byte("balanceOf(address)"))[:4]) || bytes.Equal(m.Data[:4], crypto.Keccak256([]byte("allowance(address,address)"))[:4])) {
		p.substitutions++
		return common.LeftPadBytes(p.amount.Bytes(), 32), nil
	}
	return raw, nil
}
func liveCachedPreflight(t *testing.T, ctx context.Context, client *ethclient.Client, audit *submitHTTPAudit, endpoint string, chainID uint64, token common.Address) {
	t.Helper()
	protocolRPC, err := protocolrpc.NewProtocolRpcClient(endpoint, protocolrpc.ClientOptions{HTTPClient: &http.Client{Transport: auditReadOnlyTransport{base: http.DefaultTransport}, Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal("Protocol RPC configuration failed (endpoint omitted)")
	}
	deployment, err := protocolRPC.GetDeployment(ctx, chainID)
	if err != nil {
		t.Fatal("read-only deployment discovery failed (endpoint omitted)")
	}
	pool, err := chain.NewProtocol(deployment)
	if err != nil {
		t.Fatal("invalid live deployment")
	}
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		t.Fatal("live header unavailable (endpoint omitted)")
	}
	op, _ := cachedFundingValidationFixture(t)
	execution := op.execution()
	// Replace the fixture context and commitment with the actual deployment context.
	execution.Plan.Context.ChainID = new(big.Int).SetUint64(chainID)
	execution.Plan.Context.ProtocolAddress = deployment.Protocol.Address
	execution.Plan.Context.TokenAddress = token
	execution.Plan.Context.Now = new(big.Int).SetUint64(head.Time)
	execution.Plan.Block = protocolrpc.CanonicalBlock{Number: head.Number.Uint64(), Hash: head.Hash(), ParentHash: head.ParentHash, Timestamp: head.Time}
	execution.Plan.Deadline = head.Time + 600
	execution.Plan.Funding.Deadline = new(big.Int).SetUint64(execution.Plan.Deadline)
	execution.Plan.Shield.Deadline = new(big.Int).SetUint64(execution.Plan.Deadline)
	execution.Plan.Shield.Token = token
	caller, err := contracts.NewInvisiCoreCaller(deployment.Protocol.Address, client)
	if err != nil {
		t.Fatal("live caller unavailable")
	}
	execution.Plan.Funding.SigNonce, err = caller.Nonces(&bind.CallOpts{Context: ctx, BlockNumber: head.Number}, execution.Plan.Funding.Payer)
	if err != nil {
		t.Fatal("live synthetic payer nonce unavailable (endpoint omitted)")
	}
	for i, out := range execution.Plan.Outputs {
		commitment, e := primitives.ComputeNoteCommitment(primitives.BigIntToElement(execution.Plan.Context.ChainID), primitives.BigIntToElement(deployment.Protocol.Address.Big()), primitives.BigIntToElement(out.Identity.Big()), primitives.BigIntToElement(token.Big()), primitives.BigIntToElement(out.Amount))
		if e != nil {
			t.Fatal(e)
		}
		execution.Plan.Shield.NoteCommitments[i] = commitment.Bytes()
	}
	synthetic := &auditSyntheticFundingProvider{Provider: client, token: token, amount: execution.Plan.Funding.Amount}
	execution.Runtime = &transaction.Runtime{Protocol: pool, Options: transaction.RuntimeOptions{InvisiOptions: transaction.InvisiOptions{Provider: synthetic}}}
	typed, err := execution.FundingTypedData()
	if err != nil {
		t.Fatal(err)
	}
	request, err := primitives.NewSigningRequest(typed)
	if err != nil {
		t.Fatal(err)
	}
	execution.Plan.Funding.Signature, err = signFixtureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		for _, baseline := range []bool{true, false} {
			execution.Runtime.Options.FundingVerifier = nil
			label := "optimized"
			expected := 10
			if baseline {
				// An explicit built-in verifier executes the unchanged public
				// chain check, reproducing the original default verifier path.
				execution.Runtime.Options.FundingVerifier = transaction.GethFundingVerifier{Provider: synthetic}
				label = "baseline_equivalent"
				expected = 11
			}
			// This configured deployment includes an address registry check.
			if deployment.Registry == nil {
				expected--
			}
			synthetic.substitutions = 0
			audit.reset()
			start := time.Now()
			if err = execution.ValidateExport(ctx); err != nil {
				t.Fatal("live preflight failed (endpoint omitted)")
			}
			audit.report(t, fmt.Sprintf("live_cached_shield_%s_round_%d", label, round), start)
			if audit.requests != expected {
				t.Fatalf("live preflight HTTP=%d want %d", audit.requests, expected)
			}
			if synthetic.substitutions != 2 {
				t.Fatalf("funding substitutions=%d want2", synthetic.substitutions)
			}
		}
	}
	t.Log("LIVE_PREFLIGHT synthetic public key1 wallet; actual deployment/code/VERSION/registry/head/nonce/balance/allowance RPC reads; only balance+allowance results replaced locally with synthetic amount; no relay call")
}

func TestLiveAuditTransportBlocksSubmissions(t *testing.T) {
	for _, method := range []string{"broadcaster_submitTransaction", "eth_sendRawTransaction", "eth_sendTransaction"} {
		t.Run(method, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": []any{}})
			request, err := http.NewRequest(http.MethodPost, "https://unused.example", bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			// A nil base proves rejection occurs before any network transport access.
			if _, err = (auditReadOnlyTransport{}).RoundTrip(request); err == nil {
				t.Fatal("live diagnostic allowed submission")
			}
		})
	}
}
