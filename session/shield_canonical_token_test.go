package session

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/protocol"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

func TestLiveShieldCanonicalTokenFence(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		rpcCode                          int
		rpcMessage                       string
		wantHash, wantNumber, wantHeader int
		reject                           bool
	}{
		{name: "canonical", wantHash: 1, wantHeader: 1},
		{name: "checkpoint", wantHash: 1, wantHeader: 2},
		{name: "reorg", rpcCode: -32000, wantHash: 1, wantHeader: 1, reject: true},
		{name: "unknown hash", rpcCode: -32001, wantHash: 1, wantHeader: 1, reject: true},
		{name: "transport", wantHash: 1, wantHeader: 1, reject: true},
		{name: "malformed code", wantHash: 1, wantHeader: 1, reject: true},
		{name: "empty code", wantHash: 1, wantHeader: 1, reject: true},
		{name: "unsupported block selector", rpcCode: -32602, wantHash: 1, wantNumber: 1, wantHeader: 2},
		{name: "legacy string selector", rpcCode: -32602, rpcMessage: "invalid argument 1: json: cannot unmarshal object into Go value of type string", wantHash: 1, wantNumber: 1, wantHeader: 2},
		{name: "invalid params", rpcCode: -32602, wantHash: 1, wantHeader: 1, reject: true},
		{name: "unknown block at height", rpcCode: -32602, wantHash: 1, wantHeader: 1, reject: true},
		{name: "hash is not currently canonical", rpcCode: -32602, wantHash: 1, wantHeader: 1, reject: true},
		{name: "state is pruned", rpcCode: -32602, wantHash: 1, wantHeader: 1, reject: true},
		{name: "malformed block hash", rpcCode: -32602, wantHash: 1, wantHeader: 1, reject: true},
		{name: "legacy reorg", rpcCode: -32602, rpcMessage: "unsupported block selector", wantHash: 1, wantNumber: 1, wantHeader: 2, reject: true},
		{name: "method unsupported", rpcCode: -32601, wantHash: 1, wantNumber: 1, wantHeader: 2},
		{name: "retired epoch", wantHeader: 1, reject: true},
		{name: "wrong network", reject: true},
		{name: "changed checkpoint", wantHeader: 2, reject: true},
		{name: "epoch between reads", wantHeader: 1, reject: true},
		{name: "cancel at final fence", wantHash: 1, wantHeader: 1, reject: true},
		{name: "invalidated during prepare", wantHash: 1, wantHeader: 1, reject: true},
		{name: "permit", wantNumber: 1, wantHeader: 2},
		{name: "opaque recipient", wantNumber: 1, wantHeader: 2},
		{name: "custom verifier", wantNumber: 1, wantHeader: 2},
		{name: "caller authorization", wantNumber: 1, wantHeader: 2},
		{name: "custom provider", wantNumber: 1, wantHeader: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, token, _ := testSession(t)
			head := s.options.Provider.(sessionProvider).head
			if tc.name != "checkpoint" && tc.name != "changed checkpoint" {
				if err := s.options.Store.Update(ctx, s.scope, func(state *WalletState) error { state.ObservedThrough = nil; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			counts := map[string]int{}
			var binding *chain.HTTPBinding
			var preparing bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     json.RawMessage
					Method string
					Params []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				result := map[string]any{"jsonrpc": "2.0", "id": req.ID}
				switch req.Method {
				case "eth_chainId":
					result["result"] = "0x7a69"
					if preparing && tc.name == "wrong network" {
						result["result"] = "0x1"
					}
				case "eth_getBlockByNumber":
					counts["header"]++
					result["result"] = head
					if string(req.Params[0]) != "\"latest\"" && (tc.name == "legacy reorg" || tc.name == "changed checkpoint") {
						result["result"] = fixtureHeader(10, 101)
					}
				case "eth_call":
					var call struct {
						To    common.Address
						Input hexutil.Bytes
					}
					if err := json.Unmarshal(req.Params[0], &call); err != nil {
						t.Error(err)
					}
					result["result"] = fmt.Sprintf("0x%064x", 1)
					if preparing && len(call.Input) != 4 {
						if call.To != s.scope.ProtocolAddress || len(call.Input) != 36 || string(call.Input[:4]) != string(crypto.Keccak256([]byte("nonces(address)"))[:4]) || common.BytesToAddress(call.Input[4:]) != s.wallet.OwnerAddress() || string(req.Params[1]) != "\"0xa\"" {
							t.Errorf("unexpected nonce call %s", req.Params)
						}
						counts["nonce"]++
						result["result"] = fmt.Sprintf("0x%064x", 14)
						if tc.name == "retired epoch" {
							binding.Invalidate()
						}
					}
				case "eth_getCode":
					result["result"] = "0x6000"
					var address common.Address
					if err := json.Unmarshal(req.Params[0], &address); err != nil {
						t.Error(err)
					}
					if address == s.scope.ProtocolAddress {
						break
					}
					if address != token {
						t.Errorf("unexpected token %s", address)
					}
					if string(req.Params[1]) == "\"0xa\"" {
						counts["number"]++
						break
					}
					var selector struct {
						BlockHash        common.Hash
						RequireCanonical bool
					}
					if err := json.Unmarshal(req.Params[1], &selector); err != nil || !selector.RequireCanonical || selector.BlockHash != head.Hash() {
						t.Errorf("incorrect canonical selector %s", req.Params[1])
					}
					counts["hash"]++
					if counts["nonce"] != 1 {
						t.Error("canonical token fence did not follow nonce selection")
					}
					switch {
					case tc.rpcCode != 0:
						delete(result, "result")
						message := tc.rpcMessage
						if message == "" {
							message = tc.name
						}
						result["error"] = map[string]any{"code": tc.rpcCode, "message": message}
					case tc.name == "cancel at final fence":
						cancel()
					case tc.name == "invalidated during prepare":
						if err := s.options.Store.Update(ctx, s.scope, func(state *WalletState) error {
							state.Invalidated = true
							return nil
						}); err != nil {
							t.Error(err)
						}
					case tc.name == "empty code":
						result["result"] = "0x"
					case tc.name == "malformed code":
						result["result"] = "0x0"
					case tc.name == "transport":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
				default:
					t.Errorf("unexpected method %s", req.Method)
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			var err error
			binding, err = chain.NewHTTPBinding(ctx, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer binding.Close()
			s.options.Provider = binding
			s.rpc = s.rpc.WithProvider(binding)
			if err = s.protocol.ValidateDeployment(ctx, binding); err != nil {
				t.Fatal(err)
			}
			if tc.name == "custom provider" {
				s.options.Provider = struct{ sdk.Provider }{binding}
			}
			if tc.name == "custom verifier" {
				s.options.FundingVerifier = &auditFundingPolicy{}
			}
			if tc.name == "epoch between reads" {
				read := s.runtime.SummaryReader
				s.runtime.SummaryReader = func(ctx context.Context) (transaction.Inventory, error) {
					state, err := read(ctx)
					binding.Invalidate()
					return state, err
				}
			}
			mu.Lock()
			preparing = true
			mu.Unlock()
			address, _ := s.wallet.GetAddress()
			input := sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}}
			if tc.name == "caller authorization" {
				input.Funding.Authorization = "caller"
			}
			if tc.name == "permit" {
				input.Funding.Permit = &protocol.TokenPermit{Deadline: big.NewInt(1000)}
			}
			if tc.name == "opaque recipient" {
				out, err := sdk.NewNoteOutput(sdk.PaymentRequest{ChainID: big.NewInt(31337), ProtocolAddress: s.scope.ProtocolAddress, TokenAddress: token, Amount: big.NewInt(30), Recipient: sdk.IdentityRecipient{Identity: common.HexToHash("0x1")}})
				if err != nil {
					t.Fatal(err)
				}
				input.Outputs = []sdk.NoteOutput{out}
			}
			op, err := s.PrepareShield(ctx, input)
			mu.Lock()
			defer mu.Unlock()
			if (err != nil) != tc.reject {
				t.Errorf("prepare error %v (want rejection %t)", err, tc.reject)
			}
			if counts["hash"] != tc.wantHash || counts["number"] != tc.wantNumber || (tc.name != "wrong network" && counts["header"] != tc.wantHeader) {
				t.Errorf("reads %v; want hash=%d heightCode=%d header=%d", counts, tc.wantHash, tc.wantNumber, tc.wantHeader)
			}
			if op != nil && tc.name != "caller authorization" && op.plan.Funding.SigNonce.Cmp(big.NewInt(14)) != 0 {
				t.Fatal("observed payer nonce changed")
			}
			state, err := s.options.Store.ReadSnapshot(context.Background(), s.scope)
			if err != nil {
				t.Fatal(err)
			}
			if (len(state.Operations) == 0) != tc.reject {
				t.Errorf("journal records=%d", len(state.Operations))
			}
			for _, note := range state.Notes {
				if note.ReservedBy != "" {
					t.Fatal("Shield reserved a private note")
				}
			}
		})
	}
}
