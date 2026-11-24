package session

// Opt-in diagnostic. Every counted request reached a real loopback HTTP server.
// Synthetic keys 01/02 are public fixture data. The final write is denied by a
// diagnostic RoundTripper before forwarding to the native HTTP transport.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type threeSDKAttempt struct {
	Service       string          `json:"service"`
	Method        string          `json:"method"`
	ElapsedMs     float64         `json:"elapsedMs"`
	OK            bool            `json:"ok"`
	BlockSelector json.RawMessage `json:"blockSelector,omitempty"`
}
type threeSDKPhase struct {
	Variant               string            `json:"variant"`
	Language              string            `json:"language"`
	Fixture               int               `json:"fixture"`
	TransactionIndex      int               `json:"transactionIndex"`
	Phase                 string            `json:"phase"`
	Method                string            `json:"method"`
	Service               string            `json:"service"`
	ElapsedMs             float64           `json:"elapsedMs"`
	OK                    bool              `json:"ok"`
	ActualHTTPAttempts    int               `json:"actualHttpAttempts"`
	SourceHashes          any               `json:"sourceHashes"`
	DeniedWrites          uint64            `json:"deniedWrites"`
	InitializationCharged bool              `json:"initializationCharged"`
	Attempts              []threeSDKAttempt `json:"attempts"`
}
type threeSDKRecorder struct {
	mu       sync.Mutex
	attempts []threeSDKAttempt
	delay    time.Duration
}

func (r *threeSDKRecorder) add(service, method string, started time.Time, ok bool, selector json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, threeSDKAttempt{service, method, float64(time.Since(started).Microseconds()) / 1000, ok, append(json.RawMessage(nil), selector...)})
}
func (r *threeSDKRecorder) take() []threeSDKAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]threeSDKAttempt{}, r.attempts...)
	r.attempts = nil
	return out
}

// Both variants use the identical read-only transport policy. The concrete
// broadcaster client retains its normal business validation methods.
type threeSDKDenyTransport struct {
	base   http.RoundTripper
	denied uint64
}

func (d *threeSDKDenyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var q struct{ Method string }
	if err = json.Unmarshal(body, &q); err != nil {
		return nil, err
	}
	if q.Method == "broadcaster_submitTransaction" || q.Method == "eth_sendRawTransaction" || q.Method == "eth_sendTransaction" {
		d.denied++
		return nil, errors.New("diagnostic denied write before outbound HTTP")
	}
	return d.base.RoundTrip(req)
}

func TestThreeSDKUnifiedActualHTTP(t *testing.T) {
	output := os.Getenv("INVISI_THREE_SDK_OUTPUT")
	if output == "" {
		t.Skip("set INVISI_THREE_SDK_OUTPUT; opt-in controlled benchmark")
	}
	variant := os.Getenv("INVISI_THREE_SDK_VARIANT")
	if variant == "" {
		variant = "after"
	}
	delay := 20
	if v := os.Getenv("INVISI_THREE_SDK_DELAY_MS"); v != "" {
		var err error
		delay, err = strconv.Atoi(v)
		if err != nil || delay < 0 {
			t.Fatal("invalid delay")
		}
	}
	mergeNonce := os.Getenv("INVISI_THREE_SDK_MERGE_NONCE") == "1"
	canonicalUnsupported := os.Getenv("INVISI_THREE_SDK_CANONICAL_UNSUPPORTED") == "1"
	anchorRequested := os.Getenv("INVISI_THREE_SDK_PREPARATION_ANCHOR") == "1"
	var anchorAdoptions []bool
	defaultService := os.Getenv("INVISI_THREE_SDK_DEFAULT_SERVICE") == "1"
	nativeServiceRequested := os.Getenv("INVISI_THREE_SDK_NATIVE_SERVICE") == "1"
	feeLockEnabled := os.Getenv("INVISI_THREE_SDK_FEE_LOCK") == "1"
	var nativeServiceAdoptions []bool
	var hashes any
	if os.Getenv("INVISI_THREE_SDK_HASHES") == "" {
		t.Fatal("INVISI_THREE_SDK_HASHES must identify the compiled source/binary manifest")
	}
	if path := os.Getenv("INVISI_THREE_SDK_HASHES"); path != "" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &hashes); e != nil {
			t.Fatal(e)
		}
	}
	var rows []threeSDKPhase
	for fixture := 1; fixture <= 3; fixture++ {
		t.Run(fmt.Sprintf("fixture_%d", fixture), func(t *testing.T) {
			ctx := context.Background()
			rec := &threeSDKRecorder{delay: time.Duration(delay) * time.Millisecond}
			phase := func(index int, name string, fn func() error) {
				started := time.Now()
				err := fn()
				elapsed := float64(time.Since(started).Microseconds()) / 1000
				attempts := rec.take()
				rows = append(rows, threeSDKPhase{Variant: variant, Language: "go", Fixture: fixture, TransactionIndex: index, Phase: name, Method: "phase-total", Service: "all", ElapsedMs: elapsed, OK: err == nil, ActualHTTPAttempts: len(attempts), SourceHashes: hashes, InitializationCharged: index == 0, Attempts: attempts})
				if err != nil {
					t.Fatal(name, err)
				}
			}
			var s *WalletSession
			var anchorAdopted atomic.Bool
			var pool *sdk.Protocol
			var provider sdk.Provider
			var opts sdk.BroadcasterOptions
			var selectedClient *broadcaster.Client
			var denyTransport *threeSDKDenyTransport
			var wallets [2]*sdk.Wallet
			token := common.HexToAddress("0x2222222222222222222222222222222222222222")
			address := common.HexToAddress("0x1111111111111111111111111111111111111111")
			fixtureProvider := &submissionProvider{sessionProvider: sessionProvider{head: fixtureHeader(10, uint64(time.Now().Unix())), leaves: &sync.Map{}}}
			contract := protocolrpc.ContractDeployment{Address: address, RuntimeCodeHash: transaction.HashCode([]byte{0x60, 0}), FromBlock: 1}
			registry := contract
			registry.Address = common.HexToAddress("0x4444444444444444444444444444444444444444")
			deployment := protocolrpc.ProtocolDeployment{ChainID: "31337", Protocol: contract, BroadcasterRegistry: &registry}
			block := protocolrpc.CanonicalBlock{Number: 10, Hash: fixtureProvider.head.Hash(), ParentHash: fixtureProvider.head.ParentHash, Timestamp: fixtureProvider.head.Time}
			relayKey, _ := crypto.HexToECDSA(strings.Repeat("03", 32))
			relaySigner := crypto.PubkeyToAddress(relayKey.PublicKey)
			feeRecipient := common.HexToAddress("0x8877")
			var endpoint string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started := time.Now()
				var req struct {
					ID     json.RawMessage
					Method string
					Params []json.RawMessage
				}
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
					return
				}
				service := "chain"
				if strings.HasPrefix(req.Method, "invisi_") {
					service = "protocol"
				}
				if strings.HasPrefix(req.Method, "broadcaster_") {
					service = "broadcaster"
				}
				method := req.Method
				var result any
				var err error
				errorCode := -32000
				time.Sleep(rec.delay)
				switch req.Method {
				case "eth_chainId":
					result = "0x7a69"
				case "eth_getCode":
					if len(req.Params) != 2 {
						err = errors.New("getCode requires target and selector")
					} else if len(req.Params[1]) > 0 && req.Params[1][0] == '{' {
						var target common.Address
						var selector struct {
							BlockHash        common.Hash `json:"blockHash"`
							RequireCanonical bool        `json:"requireCanonical"`
						}
						if e := json.Unmarshal(req.Params[0], &target); e != nil {
							err = e
						}
						if e := json.Unmarshal(req.Params[1], &selector); e != nil {
							err = e
						}
						if target != token || selector.BlockHash != block.Hash || !selector.RequireCanonical {
							err = errors.New("wrong canonical token selector")
						}
						if err == nil && canonicalUnsupported {
							err = errors.New("unsupported block selector")
							errorCode = -32602
						}
					}
					result = "0x6000"
				case "eth_getBlockByNumber":
					result = fixtureProvider.head
				case "eth_call":
					var call struct {
						To    common.Address
						Input hexutil.Bytes
						Data  hexutil.Bytes
					}
					err = json.Unmarshal(req.Params[0], &call)
					data := call.Input
					if len(data) == 0 {
						data = call.Data
					}
					method += ":" + common.Bytes2Hex(data[:4])
					var at *big.Int
					if err == nil && len(req.Params) > 1 && string(req.Params[1]) != `"latest"` {
						var text string
						err = json.Unmarshal(req.Params[1], &text)
						if err == nil {
							at, err = hexutil.DecodeBig(text)
						}
					}
					if mergeNonce && len(data) >= 4 && bytes.Equal(data[:4], crypto.Keccak256([]byte("nonces(address)"))[:4]) {
						if anchorAdopted.Load() {
							if at != nil || len(req.Params) < 2 || string(req.Params[1]) != `"latest"` {
								err = errors.New("retained-anchor nonce must read latest state")
							}
						} else if at == nil || at.Cmp(big.NewInt(10)) != 0 {
							err = errors.New("merged nonce must use freshly read prepare block")
						}
					}
					var out []byte
					if err == nil {
						out, err = fixtureProvider.CallContract(r.Context(), ethereum.CallMsg{To: &call.To, Data: data}, at)
					}
					result = hexutil.Encode(out)
				case "invisi_getDeployment":
					result = map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": deployment}
				case "invisi_getBroadcasters":
					result = protocolrpc.BroadcasterPage{Block: block, Broadcasters: []protocolrpc.RegisteredBroadcaster{{Operator: relaySigner, Endpoint: endpoint, QuoteSigner: relaySigner, FeeRecipient: feeRecipient, Enabled: true, Block: block}}}
				case "broadcaster_getTokenSupport", "broadcaster_getStatus":
					status := map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": "31337", "protocol": address.Hex(), "sender": relaySigner.Hex(), "feeRecipient": feeRecipient.Hex(), "submissionHalted": false, "provenance": map[string]any{"enabled": false}, "tokenAddress": token.Hex(), "supported": true, "scheduling": map[string]any{"enabled": false}}
					if feeLockEnabled {
						status["feeLock"] = broadcaster.FeeLockCapabilities{Version: "1", Model: "core-gas-v1.3.4", Core: true, Router: false}
					}
					result = status
				case "broadcaster_lockFee":
					if !feeLockEnabled {
						err = errors.New("unexpected fee lock request")
						break
					}
					lock := broadcaster.FeeLock{Token: token.Hex(), FeeRecipient: feeRecipient.Hex(), Numerator: "1", Denominator: "1", GasPrice: "1", MarkupBPS: "0", GasMarginBPS: "0", MaxGas: "1000000", Deadline: strconv.FormatUint(fixtureProvider.head.Time+3600, 10), Model: "core-gas-v1.3.4"}
					digest, e := broadcaster.FeeLockDigest(settlement.QuoteDomain{ChainID: big.NewInt(31337), Protocol: address}, lock)
					if e != nil {
						err = e
						break
					}
					sig, e := crypto.Sign(digest.Bytes(), relayKey)
					err = e
					result = broadcaster.SignedFeeLock{Lock: lock, Signature: hexutil.Encode(sig)}
				case "broadcaster_estimateQuote":
					if feeLockEnabled {
						var param struct{ FeeLock *broadcaster.SignedFeeLock }
						err = json.Unmarshal(req.Params[0], &param)
						if err == nil && param.FeeLock == nil {
							err = errors.New("advertised tariff missing from estimate")
						}
						if err == nil {
							err = broadcaster.VerifyFeeLock(*param.FeeLock, broadcaster.Trust{ChainID: big.NewInt(31337), Protocol: address, Signer: relaySigner}, token, feeRecipient, uint64(time.Now().Unix()))
						}
					}
					result = broadcaster.Fee{FeeRecipient: feeRecipient, FeeAmount: "5", MaxGas: "1000000"}
				case "broadcaster_getQuote":
					var param struct {
						Data    string
						FeeLock *broadcaster.SignedFeeLock
					}
					err = json.Unmarshal(req.Params[0], &param)
					if err == nil && feeLockEnabled {
						if param.FeeLock == nil {
							err = errors.New("advertised tariff missing from quote")
						} else {
							err = broadcaster.VerifyFeeLock(*param.FeeLock, broadcaster.Trust{ChainID: big.NewInt(31337), Protocol: address, Signer: relaySigner}, token, feeRecipient, uint64(time.Now().Unix()))
						}
						if err != nil {
							break
						}
					}
					data := common.FromHex(param.Data)
					i, e := broadcaster.DecodeIntent(data)
					if e != nil {
						err = e
						break
					}
					h, e := broadcaster.RequestHash(data)
					if e != nil {
						err = e
						break
					}
					quote := broadcaster.Quote{Token: i.Token, FeeRecipient: i.FeeRecipient, FeeAmount: i.FeeAmount.String(), Deadline: i.Deadline.String(), MaxGas: "1000000", RequestHash: h, Execution: "direct"}
					typed, e := broadcaster.QuoteTypedData(quote, broadcaster.Trust{ChainID: big.NewInt(31337), Protocol: address, Signer: relaySigner})
					if e != nil {
						err = e
						break
					}
					digest, _, e := apitypes.TypedDataAndHash(typed)
					if e != nil {
						err = e
						break
					}
					sig, e := crypto.Sign(digest, relayKey)
					err = e
					result = broadcaster.SignedQuote{Quote: quote, Signature: hexutil.Encode(sig)}
				default:
					err = fmt.Errorf("unexpected method %s", req.Method)
					t.Error(err)
				}
				var selector json.RawMessage
				if req.Method == "eth_getBlockByNumber" && len(req.Params) > 0 {
					selector = req.Params[0]
				}
				if (req.Method == "eth_call" || req.Method == "eth_getCode") && len(req.Params) > 1 {
					selector = req.Params[1]
				}
				rec.add(service, method, started, err == nil, selector)
				response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
				if err != nil {
					delete(response, "result")
					response["error"] = map[string]any{"code": errorCode, "message": err.Error()}
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			server := httptest.NewTLSServer(handler)
			t.Cleanup(server.Close)
			endpoint = server.URL
			chainServer := httptest.NewServer(handler)
			t.Cleanup(chainServer.Close)
			phase(0, "coldSetup", func() error {
				var e error
				for i := range wallets {
					wallets[i], e = localTestWallet("0x" + strings.Repeat(fmt.Sprintf("%02d", i+1), 32))
					if e != nil {
						return e
					}
				}
				provider = threeSDKProvider(t, chainServer.URL)
				serviceURL := endpoint + "/v1"
				serviceOptions := protocolrpc.ClientOptions{HTTPClient: server.Client()}
				if defaultService {
					serviceURL = chainServer.URL + "/v1"
					serviceOptions = protocolrpc.ClientOptions{}
				}
				nativeAdopted := false
				if nativeServiceRequested {
					option := reflect.ValueOf(&serviceOptions).Elem().FieldByName("UseNativeHTTPTransport")
					if option.IsValid() {
						option.SetBool(true)
						nativeAdopted = true
					}
				}
				nativeServiceAdoptions = append(nativeServiceAdoptions, nativeAdopted)
				rpc, e := sdk.NewProtocolRpcClient(serviceURL, serviceOptions)
				if e != nil {
					return e
				}
				pool, e = sdk.ProtocolFromRPC(ctx, rpc, provider)
				if e != nil {
					return e
				}
				s, e = Connect(ctx, Options{Invisi: sdk.NewInvisi(sdk.InvisiOptions{Provider: provider, ProtocolRPC: rpc}), Wallet: wallets[0], Protocol: pool, Store: NewMemoryWalletStore(), Namespace: fmt.Sprintf("three-sdk-%d", fixture)})
				denyTransport = &threeSDKDenyTransport{base: server.Client().Transport}
				opts = sdk.BroadcasterOptions{TokenAddress: token, ClientOptions: broadcaster.Options{HTTPClient: &http.Client{Transport: denyTransport}}}
				return e
			})
			phase(0, "initialization", func() error {
				var clients []*broadcaster.Client
				var e error
				if init, ok := any(pool).(interface {
					InitializeBroadcasters(context.Context, sdk.BroadcasterOptions) ([]*broadcaster.Client, error)
				}); ok {
					clients, e = init.InitializeBroadcasters(ctx, opts)
				} else {
					clients, e = pool.GetBroadcasters(ctx, opts)
				}
				if e != nil {
					return e
				}
				if len(clients) != 1 {
					return fmt.Errorf("initialized clients=%d", len(clients))
				}
				selectedClient = clients[0]
				if anchorRequested {
					if init, ok := any(s).(interface {
						InitializeShieldPreparation(context.Context, common.Address) error
					}); ok {
						if e := init.InitializeShieldPreparation(ctx, token); e != nil {
							return e
						}
						anchorAdopted.Store(true)
					}
				}
				return nil
			})
			anchorAdoptions = append(anchorAdoptions, anchorAdopted.Load())
			var firstOperationID string
			var firstJournal []byte
			for index := 1; index <= 2; index++ {
				wallet := wallets[index-1]
				fixtureProvider.nonce = big.NewInt(int64(index * 7))
				var nonce *big.Int
				var op *Operation
				var client *broadcaster.Client
				var sig []byte
				var denial func() uint64
				phase(index, "noncePrerequisite", func() error {
					if mergeNonce {
						return nil
					}
					if reader, ok := any(pool).(interface {
						ReadAuthorizationNonce(context.Context, protocolrpc.ChainProvider, common.Address) (*big.Int, error)
					}); ok {
						var e error
						nonce, e = reader.ReadAuthorizationNonce(ctx, provider, wallet.OwnerAddress())
						return e
					}
					return nil
				})
				phase(index, "prepare", func() error {
					recipient, e := wallets[1].GetAddress()
					if e != nil {
						return e
					}
					out, e := sdk.NewNoteOutput(sdk.PaymentRequest{ChainID: big.NewInt(31337), ProtocolAddress: address, TokenAddress: token, Amount: big.NewInt(30), Recipient: sdk.InvisiRecipient{Address: recipient}})
					if e != nil {
						return e
					}
					op, e = s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: []sdk.NoteOutput{out}, Funding: sdk.FundingSource{Payer: wallet.OwnerAddress(), Amount: big.NewInt(30), SigNonce: nonce}, Deadline: uint64(time.Now().Unix() + 1200)})
					return e
				})
				phase(index, "discoveryLookup", func() error {
					clients, e := pool.GetBroadcasters(ctx, opts)
					if e != nil {
						return e
					}
					if len(clients) != 1 {
						return fmt.Errorf("clients=%d", len(clients))
					}
					if clients[0].Configuration() != selectedClient.Configuration() {
						return errors.New("discovery changed initialized broadcaster binding")
					}
					client = selectedClient
					denial = func() uint64 { return denyTransport.denied }
					return nil
				})
				phase(index, "quote", func() error { return client.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}) })
				phase(index, "fundingAuthorizationAndSign", func() error {
					req, e := op.ExportFundingAuthorization(ctx)
					if e != nil {
						return e
					}
					typed, e := req.TypedData()
					if e != nil {
						return e
					}
					digest, _, e := apitypes.TypedDataAndHash(typed)
					if e != nil {
						return e
					}
					key, e := crypto.HexToECDSA(strings.Repeat(fmt.Sprintf("%02d", index), 32))
					if e != nil {
						return e
					}
					sig, e = crypto.Sign(digest, key)
					if e != nil {
						return e
					}
					op, e = op.WithFundingSignature(ctx, sig)
					return e
				})
				prior := denial()
				phase(index, "submitToDeniedBroadcast", func() error {
					_, e := client.Submit(ctx, op)
					if e == nil {
						return errors.New("write unexpectedly succeeded")
					}
					if denial() != prior+1 {
						return fmt.Errorf("did not reach innermost denied write: %w", e)
					}
					return nil
				})
				rows[len(rows)-1].DeniedWrites = 1
				if op.plan.Funding.Amount.Cmp(big.NewInt(35)) != 0 || op.plan.Outputs[0].Amount.Cmp(big.NewInt(30)) != 0 || op.plan.Funding.SigNonce.Cmp(fixtureProvider.nonce) != 0 {
					t.Fatal("business/funding amounts differ from scenario contract")
				}
				if anchorAdopted.Load() && (op.plan.Version < 2 || op.plan.SubmissionValidation != "required") {
					t.Fatal("retained online preparation must preserve its distinct recovery mode")
				}
				state, e := s.options.Store.ReadSnapshot(ctx, s.scope)
				if e != nil {
					t.Fatal(e)
				}
				if index == 1 {
					firstOperationID = op.ID()
					firstJournal, e = json.Marshal(state.Operations[firstOperationID])
					if e != nil {
						t.Fatal(e)
					}
				} else {
					retained, e := json.Marshal(state.Operations[firstOperationID])
					if e != nil {
						t.Fatal(e)
					}
					if !bytes.Equal(firstJournal, retained) {
						t.Fatal("repeat flow changed first journal")
					}
				}

			}
		})
	}
	raw, err := json.MarshalIndent(map[string]any{"schemaVersion": 1, "scenario": "ordinary-funded-self-contained-shield", "variant": variant, "language": "go", "artificialResponseDelayMs": delay, "boundary": "nonce prerequisite + prepare + lookup + quote + authorization/sign + submit through innermost denial", "sourceHashes": hashes, "nonceMergedIntoPrepare": mergeNonce, "canonicalSelectorUnsupported": canonicalUnsupported, "preparationAnchorRequested": anchorRequested, "preparationAnchorAdoptions": anchorAdoptions, "defaultServiceHTTP": defaultService, "nativeServiceRequested": nativeServiceRequested, "feeLockEnabled": feeLockEnabled, "nativeServiceAdoptions": nativeServiceAdoptions, "resumePolicy": "normal flow uses in-memory native local quote receipt; JSON restore loses unsigned local receipt and conservatively revalidates; separate recovery regressions cover restore", "rows": rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
