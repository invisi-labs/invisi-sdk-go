package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-sdk-go/protocol"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type shieldAnchorWire struct {
	mu                            sync.Mutex
	reads                         []string
	wrongNetwork, emptyCode       bool
	nonce                         uint64
	noncePayer                    common.Address
	duringNonce                   func()
	switchBranchOnPriorRead       bool
	switchedBranch                bool
	duringHeader                  func()
	duringToken                   func()
	selectorError                 string
	changedHeight, malformedNonce bool
}

func anchorWireSession(t *testing.T) (*WalletSession, common.Address, *chain.HTTPBinding, *shieldAnchorWire) {
	t.Helper()
	s, token, _ := testSession(t)
	head := s.options.Provider.(sessionProvider).head
	wire := &shieldAnchorWire{nonce: 7}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			t.Error(err)
			return
		}
		wire.mu.Lock()
		var result any
		var rpcError any
		read := q.Method
		var hook func()
		switch q.Method {
		case "eth_chainId":
			result = "0x7a69"
			if wire.wrongNetwork {
				result = "0x1"
			}
		case "eth_getBlockByNumber":
			result = head
			if wire.duringHeader != nil {
				hook = wire.duringHeader
				wire.duringHeader = nil
			}
			var number string
			if json.Unmarshal(q.Params[0], &number) == nil && number != "latest" {
				if n, err := hexutil.DecodeUint64(number); err == nil && n != 10 {
					result = fixtureHeader(n, head.Time)
				}
			}
			if wire.switchBranchOnPriorRead && number == "0x9" {
				wire.switchedBranch = true
				result = fixtureHeader(9, 101)
			}
			if wire.switchedBranch && number == "0xa" {
				result = fixtureHeader(10, 101)
			}
			if wire.changedHeight && string(q.Params[0]) != "\"latest\"" {
				result = fixtureHeader(10, 101)
			}
			read += "/" + string(q.Params[0])
		case "eth_getCode":
			var addr common.Address
			_ = json.Unmarshal(q.Params[0], &addr)
			read += "/" + addr.Hex()
			result = "0x6000"
			if addr == token && wire.duringToken != nil {
				hook = wire.duringToken
				wire.duringToken = nil
			}
			if addr == token && len(q.Params[1]) > 0 && q.Params[1][0] == '{' && wire.selectorError != "" {
				rpcError = map[string]any{"code": -32602, "message": wire.selectorError}
			}
			if addr == token && wire.emptyCode {
				result = "0x"
			}
		case "eth_call":
			var call struct {
				To    common.Address
				Input hexutil.Bytes
			}
			_ = json.Unmarshal(q.Params[0], &call)
			result = fmt.Sprintf("0x%064x", 1)
			if len(call.Input) == 36 && string(call.Input[:4]) == string(crypto.Keccak256([]byte("isNullifierSpent(bytes32)"))[:4]) {
				result = fmt.Sprintf("0x%064x", 0)
			}
			if len(call.Input) == 36 && string(call.Input[:4]) == string(crypto.Keccak256([]byte("nonces(address)"))[:4]) {
				wire.noncePayer = common.BytesToAddress(call.Input[4:])
				read += "/nonce/" + string(q.Params[1])
				result = fmt.Sprintf("0x%064x", wire.nonce)
				if wire.malformedNonce {
					result = "0x01"
				}
				hook = wire.duringNonce
			}
		default:
			t.Errorf("unexpected wire method %s", q.Method)
		}
		wire.reads = append(wire.reads, read)
		wire.mu.Unlock()
		if hook != nil {
			hook()
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}
		if rpcError != nil {
			delete(reply, "result")
			reply["error"] = rpcError
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(server.Close)
	b, err := chain.NewHTTPBinding(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	s.options.Provider = b
	s.rpc = s.rpc.WithProvider(b)
	return s, token, b, wire
}

func initShield(t *testing.T, s *WalletSession, ctx context.Context, token common.Address) error {
	t.Helper()
	i, ok := any(s).(interface {
		InitializeShieldPreparation(context.Context, common.Address) error
	})
	if !ok {
		t.Fatal("managed session cannot initialize a Shield preparation anchor")
	}
	return i.InitializeShieldPreparation(ctx, token)
}
func anchorInput(s *WalletSession, token, payer common.Address) sdk.ShieldPreparation {
	output, err := sdk.NewNoteOutput(sdk.PaymentRequest{ChainID: new(big.Int).SetUint64(s.scope.ChainID), ProtocolAddress: s.scope.ProtocolAddress, TokenAddress: token, Amount: big.NewInt(30), Recipient: sdk.InvisiRecipient{Address: walletAddress(s.wallet)}})
	if err != nil {
		panic(err)
	}
	return sdk.ShieldPreparation{TokenAddress: token, Outputs: []sdk.NoteOutput{output}, Funding: sdk.FundingSource{Payer: payer, Amount: big.NewInt(30)}}
}
func TestInitializedShieldUsesHistoricalBlockAndFreshPayerNonce(t *testing.T) {
	ctx := context.Background()
	s, token, _, wire := anchorWireSession(t)
	if err := initShield(t, s, ctx, token); err != nil {
		t.Fatal(err)
	}
	for i, nonce := range []uint64{7, 14} {
		payer := common.BigToAddress(big.NewInt(int64(i + 1)))
		wire.mu.Lock()
		wire.reads = nil
		wire.nonce = nonce
		wire.mu.Unlock()
		started := time.Now().Unix()
		op, err := s.PrepareShield(ctx, anchorInput(s, token, payer))
		if err != nil {
			t.Fatal(err)
		}
		if op.plan.Block.Timestamp != 100 || op.plan.Context.Now.Int64() < started || op.plan.Deadline != op.plan.Context.Now.Uint64()+300 || op.Preview().SubmissionValidation != "required" {
			t.Fatalf("incorrect historical construction facts: block=%+v now=%s deadline=%d validation=%q", op.plan.Block, op.plan.Context.Now, op.plan.Deadline, op.Preview().SubmissionValidation)
		}
		wire.mu.Lock()
		if len(wire.reads) != 2 || wire.reads[0] != "eth_chainId" || wire.reads[1] != "eth_call/nonce/\"latest\"" || wire.noncePayer != payer || op.plan.Funding.SigNonce.Uint64() != nonce {
			t.Errorf("payer=%s nonce=%v reads=%v", wire.noncePayer, op.plan.Funding.SigNonce, wire.reads)
		}
		wire.mu.Unlock()
	}
}
func TestInitializedShieldRetirementBeforeReservation(t *testing.T) {
	for _, scenario := range []string{"during nonce", "at wallet update", "checkpoint at wallet update", "cancellation at wallet update", "network mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, token, b, wire := anchorWireSession(t)
			if err := initShield(t, s, ctx, token); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "during nonce":
				wire.duringNonce = b.Invalidate
			case "network mismatch":
				wire.wrongNetwork = true
			default:
				s.options.Store = &anchorHookStore{WalletStore: s.options.Store, before: func(state *WalletState) {
					switch scenario {
					case "at wallet update":
						b.Invalidate()
					case "checkpoint at wallet update":
						state.CheckedThrough = state.ObservedThrough
					default:
						cancel()
					}
				}}
			}
			if op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress())); err == nil || op != nil {
				t.Fatal("retired operation was prepared")
			}
			state, err := s.options.Store.ReadSnapshot(context.Background(), s.scope)
			if err != nil || len(state.Operations) != 0 {
				t.Fatalf("records=%d error=%v", len(state.Operations), err)
			}
		})
	}
}

type anchorHookStore struct {
	WalletStore
	before func(*WalletState)
}

func (h *anchorHookStore) Update(ctx context.Context, scope WalletScope, f func(*WalletState) error) error {
	return h.WalletStore.Update(ctx, scope, func(v *WalletState) error { h.before(v); return f(v) })
}

func TestFailedShieldInitializationRetiresPreviousReceipt(t *testing.T) {
	s, token, _, wire := anchorWireSession(t)
	ctx := context.Background()
	if err := initShield(t, s, ctx, token); err != nil {
		t.Fatal(err)
	}
	wire.emptyCode = true
	if err := initShield(t, s, ctx, token); err == nil {
		t.Fatal("initialization accepted missing token code")
	}
	if op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress())); err == nil || op != nil {
		t.Fatal("failed initialization retained historical authority")
	}
}

func TestSynchronizedShieldUsesOwnedCaptureOnly(t *testing.T) {
	s, token, _, wire := anchorWireSession(t)
	ctx := context.Background()
	if err := s.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	wire.mu.Lock()
	wire.reads = nil
	wire.nonce = 14
	wire.mu.Unlock()
	op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
	if err != nil {
		t.Fatal(err)
	}
	if op.plan.Funding.SigNonce.Uint64() != 14 || !op.plan.RetainedShieldAnchor {
		t.Fatalf("synchronized Shield used historical nonce %s or missing live provenance", op.plan.Funding.SigNonce)
	}
	wire.mu.Lock()
	if len(wire.reads) != 2 || wire.reads[1] != "eth_call/nonce/\"latest\"" {
		t.Errorf("sync Shield reads %v", wire.reads)
	}
	wire.mu.Unlock()
	// A new session may still use the legacy offline snapshot. It never
	// acquires the native initialized preparation mode from that snapshot.
	reopened, err := Connect(ctx, Options{Invisi: sdk.NewInvisi(s.options.InvisiOptions), Store: s.options.Store, Wallet: s.wallet, Protocol: s.protocol, Namespace: s.scope.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := reopened.PrepareShield(ctx, anchorInput(reopened, token, s.wallet.OwnerAddress()))
	if err != nil {
		t.Fatal(err)
	}
	if probe.plan.RetainedShieldAnchor {
		t.Fatal("persisted snapshot minted in-memory native authority")
	}
}

func TestRetiredShieldAnchorCannotDelegateFirstSend(t *testing.T) {
	ctx := context.Background()
	op, client, audit, fixture, s := ownedNativeShieldFixture(t)
	if !op.plan.RetainedShieldAnchor {
		t.Fatal("expected an operation from successful native refresh")
	}
	// A second session can reset this wallet to the same public generation.
	if err := s.options.Store.Update(ctx, s.scope, func(v *WalletState) error { v.Scan.ResetRevision = v.Revision + 1; return nil }); err != nil {
		t.Fatal(err)
	}
	fixture.nonce = big.NewInt(99)
	audit.reset()
	if _, err := client.Submit(ctx, op); err == nil {
		t.Fatalf("stale anchor delegated first submission, reads=%v", audit.methods)
	}
	if op.Status() != "ready" {
		t.Fatalf("unexpected status %s", op.Status())
	}
}

func TestRetainedShieldJournalRequiresLiveChecksBeforeFirstSend(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"before quote", "quoted", "ready", "manual export"} {
		t.Run(stage, func(t *testing.T) {
			op, c, audit, fixture, s := ownedNativeShieldFixtureMode(t, stage == "ready" || stage == "manual export")
			if stage == "before quote" {
				token := op.plan.Context.TokenAddress
				var err error
				op, err = s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
				if err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := s.Resume(ctx, op.ID())
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before quote" {
				if err = c.Quote(ctx, resumed, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
					t.Fatal(err)
				}
			}
			fixture.nonce = big.NewInt(99)
			audit.reset()
			switch stage {
			case "ready":
				_, err = c.Submit(ctx, resumed)
			case "manual export":
				_, err = resumed.ExportTransaction(ctx)
			default:
				_, err = resumed.ExportFundingAuthorization(ctx)
			}
			if err == nil || audit.methods["eth_call"] < 1 {
				t.Fatalf("restored pre-first-send skipped stale nonce validation: err=%v reads=%v", err, audit.methods)
			}
			st, err := s.options.Store.ReadSnapshot(ctx, s.scope)
			if err != nil || st.Operations[op.ID()].Externalized {
				t.Fatal("failed live validation externalized operation")
			}
		})
	}
}

func TestInitializedShieldUnknownTokenAndExplicitNonce(t *testing.T) {
	ctx := context.Background()
	s, token, _, wire := anchorWireSession(t)
	if err := initShield(t, s, ctx, token); err != nil {
		t.Fatal(err)
	}
	input := anchorInput(s, token, s.wallet.OwnerAddress())
	input.Funding.SigNonce = big.NewInt(23)
	wire.mu.Lock()
	wire.reads = nil
	wire.mu.Unlock()
	op, err := s.PrepareShield(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	wire.mu.Lock()
	if op.plan.Funding.SigNonce.Int64() != 23 || len(wire.reads) != 1 || wire.reads[0] != "eth_chainId" {
		t.Errorf("explicit selection changed or read as live nonce: %s %v", op.plan.Funding.SigNonce, wire.reads)
	}
	wire.reads = nil
	wire.mu.Unlock()
	other := common.HexToAddress("0x9876")
	op, err = s.PrepareShield(ctx, anchorInput(s, other, s.wallet.OwnerAddress()))
	if err != nil {
		t.Fatal(err)
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if op.plan.RetainedShieldAnchor || !slices.Contains(wire.reads, "eth_getCode/"+other.Hex()) || !slices.Contains(wire.reads, "eth_call/nonce/\"0xa\"") {
		t.Fatalf("missing token used initialized authority: %v", wire.reads)
	}
}

func TestInitializedShieldOlderCaptureCannotPublishAfterFailedReplacement(t *testing.T) {
	ctx := context.Background()
	s, token, _, wire := anchorWireSession(t)
	started, release := make(chan struct{}), make(chan struct{})
	wire.duringToken = func() { close(started); <-release }
	done := make(chan error, 1)
	go func() { done <- initShield(t, s, ctx, token) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("older capture stalled")
	}
	if err := initShield(t, s, ctx, common.Address{}); err == nil {
		t.Fatal("invalid initialization succeeded")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("superseded capture published after replacement failed")
	}
	wire.mu.Lock()
	wire.emptyCode = true
	wire.mu.Unlock()
	if op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress())); err == nil || op != nil {
		t.Fatal("superseded historical token survived")
	}
}

func TestRetiredShieldAnchorPreservesFrozenReplayAndKnownID(t *testing.T) {
	ctx := context.Background()
	s, token, b, wire := anchorWireSession(t)
	if err := initShield(t, s, ctx, token); err != nil {
		t.Fatal(err)
	}
	op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
	if err != nil {
		t.Fatal(err)
	}
	var sends [][]byte
	var statuses int
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, raw json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			sends = append(sends, append([]byte(nil), raw...))
		}
		if method == "broadcaster_getTransactionStatus" {
			statuses++
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	req, err := op.ExportFundingAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.WithFundingSignature(ctx, sig)
	if err != nil {
		t.Fatal(err)
	}
	dropping, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: dropScheduledSubmitResponse{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dropping.Submit(ctx, op); err == nil {
		t.Fatal("response loss swallowed")
	}
	b.Close()
	wire.mu.Lock()
	wire.reads = nil
	wire.mu.Unlock()
	for range 2 {
		op, err = s.Resume(ctx, op.ID())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Submit(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if len(sends) != 2 || !bytes.Equal(sends[0], sends[1]) || statuses != 1 || len(wire.reads) != 0 {
		t.Fatalf("replay lost exact route or packet: sends=%d status=%d reads=%v", len(sends), statuses, wire.reads)
	}
}

func TestShieldInitializationCanonicalAndCheckpointFailures(t *testing.T) {
	for _, scenario := range []string{"unsupported selector", "unsupported selector reorg", "ambiguous invalid params", "changed wallet checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, token, _, wire := anchorWireSession(t)
			switch scenario {
			case "changed wallet checkpoint":
				if err := s.options.Store.Update(ctx, s.scope, func(v *WalletState) error { v.ObservedThrough.Hash = common.HexToHash("0x1234"); return nil }); err != nil {
					t.Fatal(err)
				}
			case "ambiguous invalid params":
				wire.selectorError = "unknown block at height"
			default:
				wire.selectorError = "unsupported block selector"
				wire.changedHeight = scenario == "unsupported selector reorg"
			}
			err := initShield(t, s, ctx, token)
			if (err == nil) != (scenario == "unsupported selector") {
				t.Fatalf("capture result %v", err)
			}
			wire.mu.Lock()
			defer wire.mu.Unlock()
			wantTokenReads := 2
			if scenario == "changed wallet checkpoint" {
				wantTokenReads = 0
			}
			if scenario == "ambiguous invalid params" {
				wantTokenReads = 1
			}
			got := 0
			for _, read := range wire.reads {
				if read == "eth_getCode/"+token.Hex() {
					got++
				}
			}
			if got != wantTokenReads {
				t.Fatalf("unexpected token probes %v", wire.reads)
			}
		})
	}
}

func TestInitializedShieldRejectsBadNonceAndPublicGenerationDuringCommit(t *testing.T) {
	for _, scenario := range []string{"malformed nonce", "public generation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, token, _, wire := anchorWireSession(t)
			if err := initShield(t, s, ctx, token); err != nil {
				t.Fatal(err)
			}
			if scenario == "malformed nonce" {
				wire.malformedNonce = true
			} else {
				var once sync.Once
				s.options.Store = &anchorHookStore{WalletStore: s.options.Store, before: func(*WalletState) {
					once.Do(func() {
						if err := s.publicHistory.Store().Update(ctx, s.publicHistory.Key(), func(v *PublicHistoryState) error { v.Generation++; return nil }); err != nil {
							t.Fatal(err)
						}
					})
				}}
			}
			if op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress())); err == nil || op != nil {
				t.Fatal("unsafe preparation accepted")
			}
			state, err := s.options.Store.ReadSnapshot(ctx, s.scope)
			if err != nil || len(state.Operations) != 0 {
				t.Fatalf("unexpected journal %d %v", len(state.Operations), err)
			}
		})
	}
}

func TestFailedNativeRefreshCannotReusePriorTokenReceipt(t *testing.T) {
	ctx := context.Background()
	s, token, _, wire := anchorWireSession(t)
	if err := s.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	wire.wrongNetwork = true
	if err := s.RefreshState(ctx); err == nil {
		t.Fatal("refresh accepted wrong chain")
	}
	wire.mu.Lock()
	wire.wrongNetwork = false
	wire.emptyCode = true
	wire.mu.Unlock()
	if op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress())); err == nil || op != nil {
		t.Fatal("failed refresh reused historical token from old snapshot")
	}
}

func TestInitializedShieldRetainsExcludedPreparationPaths(t *testing.T) {
	for _, scenario := range []string{"caller", "permit", "custom verifier", "custom provider", "opaque recipient"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, token, _, wire := anchorWireSession(t)
			if err := initShield(t, s, ctx, token); err != nil {
				t.Fatal(err)
			}
			p := anchorInput(s, token, s.wallet.OwnerAddress())
			switch scenario {
			case "caller":
				p.Funding.Authorization = "caller"
			case "permit":
				p.Funding.Permit = &protocol.TokenPermit{Deadline: big.NewInt(1000)}
			case "custom verifier":
				s.options.FundingVerifier = &auditFundingPolicy{}
			case "custom provider":
				s.options.Provider = struct{ sdk.Provider }{s.options.Provider}
			case "opaque recipient":
				o, err := sdk.NewNoteOutput(sdk.PaymentRequest{ChainID: big.NewInt(31337), ProtocolAddress: s.scope.ProtocolAddress, TokenAddress: token, Amount: big.NewInt(30), Recipient: sdk.IdentityRecipient{Identity: common.HexToHash("0x1")}})
				if err != nil {
					t.Fatal(err)
				}
				p.Outputs = []sdk.NoteOutput{o}
			}
			wire.mu.Lock()
			wire.reads = nil
			wire.mu.Unlock()
			op, err := s.PrepareShield(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			wire.mu.Lock()
			defer wire.mu.Unlock()
			if op.plan.RetainedShieldAnchor || !slices.Contains(wire.reads, "eth_getCode/"+token.Hex()) {
				t.Fatalf("excluded input consumed historical token facts: %v", wire.reads)
			}
		})
	}
}

func TestRetiredShieldAnchorAtFrozenCommitStopsFirstTransport(t *testing.T) {
	for _, change := range []string{"failed reinitialization", "wallet checkpoint"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			op, client, _, _, s := ownedNativeShieldFixture(t)
			base := s.options.Store
			changed := false
			s.options.Store = &anchorHookStore{WalletStore: base, before: func(v *WalletState) {
				if changed || !v.Operations[op.ID()].Externalized {
					return
				}
				changed = true
				if change == "wallet checkpoint" {
					v.CheckedThrough = nil
					return
				}
				if err := s.InitializeShieldPreparation(ctx, common.Address{}); err == nil {
					t.Fatal("invalid replacement succeeded")
				}
			}}
			transport := &managedDenyTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, deny: func() error { return fmt.Errorf("first transport ran after receipt retired") }}
			guarded, err := broadcaster.NewClient(client.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = guarded.Submit(ctx, op); err == nil || !changed || transport.denied != 0 {
				t.Fatalf("receipt retired=%t first transport calls=%d err=%v", changed, transport.denied, err)
			}
			s.options.Store = base
			resumed, err := s.Resume(ctx, op.ID())
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Status() != "uncertain" || resumed.plan.Broadcaster.Request == nil {
				t.Fatal("frozen recovery packet lost")
			}
			oldPacket, err := json.Marshal(resumed.plan.Broadcaster.Request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Submit(ctx, resumed); err != nil {
				t.Fatal(err)
			}
			nextPacket, err := json.Marshal(resumed.plan.Broadcaster.Request)
			if err != nil || !bytes.Equal(oldPacket, nextPacket) {
				t.Fatal("recovery packet changed")
			}
		})
	}
}

func TestRestoredWalletRefreshAuthenticatesActualProtocolService(t *testing.T) {
	ctx := context.Background()
	s, token, b, _ := anchorWireSession(t)
	if err := s.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unavailable", "wrong deployment"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var q struct {
					ID     uint64
					Method string
				}
				_ = json.NewDecoder(req.Body).Decode(&q)
				requests.Add(1)
				if q.Method != "invisi_getDeployment" {
					t.Errorf("unexpected service request %s", q.Method)
				}
				if mode == "unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				d := s.protocol.Deployment()
				d.Protocol.Address = common.HexToAddress("0x9876")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": d}})
			}))
			defer server.Close()
			rpc, err := sdk.NewProtocolRpcClient(server.URL + "/v1")
			if err != nil {
				t.Fatal(err)
			}
			infra := s.options.InvisiOptions
			infra.Provider = b
			infra.ProtocolRPC = rpc
			reopened, err := Connect(ctx, Options{Invisi: sdk.NewInvisi(infra), Store: s.options.Store, Wallet: s.wallet, Protocol: s.protocol, Namespace: s.scope.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatal("fixture did not restore offline")
			}
			refreshErr := reopened.RefreshState(ctx)
			op, err := reopened.PrepareShield(ctx, anchorInput(reopened, token, s.wallet.OwnerAddress()))
			if refreshErr == nil || requests.Load() != 1 || (err == nil && op.plan.RetainedShieldAnchor) {
				t.Fatalf("untrusted protocol service promoted: refresh=%v deployment calls=%d preparation=%v", refreshErr, requests.Load(), err)
			}
		})
	}
}

func TestNativeCaptureWrongNetworkRetiresSharedBindingEpoch(t *testing.T) {
	for _, mode := range []string{"initialize", "sync"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, token, b, wire := anchorWireSession(t)
			if err := s.InitializeShieldPreparation(ctx, token); err != nil {
				t.Fatal(err)
			}
			other, err := Connect(ctx, Options{Invisi: sdk.NewInvisi(s.options.InvisiOptions), Wallet: s.wallet, Protocol: s.protocol, Store: s.options.Store, Namespace: s.scope.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			epoch := b.Epoch()
			wire.mu.Lock()
			wire.wrongNetwork = true
			wire.mu.Unlock()
			if mode == "initialize" {
				err = other.InitializeShieldPreparation(ctx, token)
			} else {
				err = other.RefreshState(ctx)
			}
			if err == nil {
				t.Fatal("wrong network accepted")
			}
			wire.mu.Lock()
			wire.wrongNetwork = false
			wire.mu.Unlock()
			if b.Epoch() == epoch {
				t.Fatal("wrong network observed through recorder did not retire the shared native binding")
			}
			op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
			if err == nil && op.plan.RetainedShieldAnchor {
				t.Fatal("another session reused a receipt from before the network mismatch")
			}
		})
	}
}

func TestNativeRefreshPreservesPriorPrivateCheckedCheckpoint(t *testing.T) {
	for _, mode := range []string{"orphan", "ahead"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, _, _, _ := anchorWireSession(t)
			n := uint64(9)
			if mode == "ahead" {
				n = 11
			}
			h := fixtureHeader(n, 100)
			saved := protocolrpc.CanonicalBlock{Number: n, Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}
			if mode == "orphan" {
				saved.Hash = common.HexToHash("0x77")
			}
			if err := s.options.Store.Update(ctx, s.scope, func(v *WalletState) error { v.CheckedThrough = &saved; return nil }); err != nil {
				t.Fatal(err)
			}
			err := s.RefreshState(ctx)
			st, readErr := s.options.Store.ReadSnapshot(ctx, s.scope)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err == nil || st.CheckedThrough == nil || *st.CheckedThrough != saved {
				t.Fatalf("refresh erased invalid private checkpoint: err=%v checked=%v", err, st.CheckedThrough)
			}
		})
	}
}

func TestActiveWalletScanCannotReplaceLaterShieldInitialization(t *testing.T) {
	for _, entry := range []string{"scan", "staged"} {
		for _, mode := range []string{"success", "failed"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				s, first, _, wire := anchorWireSession(t)
				if err := s.RefreshState(ctx); err != nil {
					t.Fatal(err)
				}
				if err := s.options.Store.Update(ctx, s.scope, func(v *WalletState) error { v.Scan.Coverage = []CoverageRange{{1, *v.CheckedThrough}}; return nil }); err != nil {
					t.Fatal(err)
				}
				started, release := make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				if entry == "scan" {
					wire.mu.Lock()
					wire.duringHeader = func() { close(started); <-release }
					wire.mu.Unlock()
					go func() { _, err := s.ScanHistory(ctx, testDeliveryDecryptor(s), ScanOptions{}); done <- err }()
				} else {
					stage, err := s.Refresh(ctx, RefreshOptions{})
					if err != nil {
						t.Fatal(err)
					}
					go func() { close(started); <-release; _, err := stage.Decrypt(ctx, testDeliveryDecryptor(s)); done <- err }()
				}
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("scan did not start")
				}
				next := common.HexToAddress("0x9876")
				if mode == "failed" {
					next = common.Address{}
				}
				initErr := s.InitializeShieldPreparation(ctx, next)
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if (initErr == nil) != (mode == "success") {
					t.Fatal(initErr)
				}
				token := next
				if mode == "failed" {
					token = first
				}
				op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
				if err != nil {
					t.Fatal(err)
				}
				if op.plan.RetainedShieldAnchor != (mode == "success") {
					t.Fatal("earlier scan replaced the later initialization result")
				}
			})
		}
	}
}

func TestRetainedShieldJournalRequiresDistinctVersion(t *testing.T) {
	ctx := context.Background()
	s, token, _, _ := anchorWireSession(t)
	if err := s.InitializeShieldPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	op, err := s.PrepareShield(ctx, anchorInput(s, token, s.wallet.OwnerAddress()))
	if err != nil {
		t.Fatal(err)
	}
	if op.plan.Version != 2 {
		t.Errorf("older version-1 SDK would accept retained plan version %d", op.plan.Version)
	}
	for _, field := range []string{"Version", "RetainedShieldAnchor"} {
		var original json.RawMessage
		if err = s.options.Store.Update(ctx, s.scope, func(v *WalletState) error {
			record := v.Operations[op.ID()]
			original = record.Plan
			var p map[string]json.RawMessage
			if e := json.Unmarshal(record.Plan, &p); e != nil {
				return e
			}
			if field == "Version" {
				p[field] = json.RawMessage("1")
			} else {
				delete(p, field)
				p["Version"] = json.RawMessage("2")
			}
			raw, e := json.Marshal(p)
			if e != nil {
				return e
			}
			record.Plan = raw
			v.Operations[op.ID()] = record
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Resume(ctx, op.ID()); err == nil {
			t.Errorf("restored inconsistent retained journal after changing %s", field)
		}
		if err = s.options.Store.Update(ctx, s.scope, func(v *WalletState) error {
			record := v.Operations[op.ID()]
			record.Plan = original
			v.Operations[op.ID()] = record
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeRefreshFencesHeadAfterMissingHistoricalCheckpointRead(t *testing.T) {
	ctx := context.Background()
	s, _, _, wire := anchorWireSession(t)
	// The wallet's lower checkpoint is on branch B. The captured head starts
	// on A. The node switches to B as its previously unread lower header loads.
	prior := fixtureHeader(9, 101)
	saved := protocolrpc.CanonicalBlock{Number: 9, Hash: prior.Hash(), ParentHash: prior.ParentHash, Timestamp: prior.Time}
	if err := s.options.Store.Update(ctx, s.scope, func(v *WalletState) error { v.CheckedThrough = &saved; return nil }); err != nil {
		t.Fatal(err)
	}
	wire.mu.Lock()
	wire.switchBranchOnPriorRead = true
	wire.mu.Unlock()
	err := s.RefreshState(ctx)
	v, readErr := s.options.Store.ReadSnapshot(ctx, s.scope)
	if readErr != nil {
		t.Fatal(readErr)
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if !wire.switchedBranch {
		t.Fatal("missing prior header was not queried")
	}
	if err == nil || v.CheckedThrough == nil || *v.CheckedThrough != saved {
		t.Fatalf("mixed-branch capture committed: err=%v reads=%v", err, wire.reads)
	}
}
