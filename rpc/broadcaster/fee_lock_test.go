package broadcaster

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

func TestFeeLockGoldenAndRejection(t *testing.T) {
	raw, err := os.ReadFile("testdata/fee-lock-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SignedFeeLock
		Digest string
		Trust  struct{ ChainID, Protocol, Signer string }
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	chain, _ := new(big.Int).SetString(v.Trust.ChainID, 10)
	trust := Trust{ChainID: chain, Protocol: common.HexToAddress(v.Trust.Protocol), Signer: common.HexToAddress(v.Trust.Signer)}
	d, err := FeeLockDigest(settlement.QuoteDomain{ChainID: chain, Protocol: trust.Protocol}, v.Lock)
	if err != nil || d.Hex() != v.Digest {
		t.Fatalf("digest: %s %v", d, err)
	}
	token := common.HexToAddress(v.Lock.Token)
	recipient := common.HexToAddress(v.Lock.FeeRecipient)
	if err = VerifyFeeLock(v.SignedFeeLock, trust, token, recipient, 1900000000); err != nil {
		t.Fatal(err)
	}
	if VerifyFeeLock(v.SignedFeeLock, trust, token, recipient, 2000000000) == nil {
		t.Fatal("accepted expired ticket")
	}
	v.Lock.GasPrice = "1"
	if VerifyFeeLock(v.SignedFeeLock, trust, token, recipient, 1900000000) == nil {
		t.Fatal("accepted forged tariff")
	}
}

func TestPreparationClientFreezesTicketWithoutMutatingSharedClient(t *testing.T) {
	raw, _ := os.ReadFile("testdata/fee-lock-vector.json")
	var v struct {
		SignedFeeLock
		Data  string
		Quote SignedQuote
		Trust struct{ ChainID, Protocol, Signer string }
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	chain, _ := new(big.Int).SetString(v.Trust.ChainID, 10)
	trust := Trust{ChainID: chain, Protocol: common.HexToAddress(v.Trust.Protocol), Signer: common.HexToAddress(v.Trust.Signer)}
	data := common.FromHex(v.Data)
	advertised := true
	lockedCalls := 0
	legacyCalls := 0
	statusCalls, tariffCalls := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var result any
		switch request.Method {
		case "broadcaster_getStatus", "broadcaster_getTokenSupport":
			if request.Method == "broadcaster_getStatus" {
				statusCalls++
			}
			status := ServiceStatus{APIVersion: "v1", ProtocolVersion: "1", ChainID: trust.ChainID.String(), Protocol: trust.Protocol, Sender: trust.Signer, FeeRecipient: common.HexToAddress(v.Lock.FeeRecipient)}
			if advertised {
				status.FeeLock = &FeeLockCapabilities{Version: "1", Model: feeLockModel, Core: true}
			}
			result = status
			if request.Method == "broadcaster_getTokenSupport" {
				result = struct {
					ServiceStatus
					TokenAddress common.Address `json:"tokenAddress"`
					Supported    bool           `json:"supported"`
				}{status, common.HexToAddress(v.Lock.Token), true}
			}
		case "broadcaster_lockFee":
			tariffCalls++
			result = v.SignedFeeLock
		case "broadcaster_estimateQuote", "broadcaster_getQuote":
			var params struct{ FeeLock *SignedFeeLock }
			_ = json.Unmarshal(request.Params[0], &params)
			if params.FeeLock != nil {
				lockedCalls++
				if params.FeeLock.Signature != v.Signature {
					t.Error("changed ticket")
				}
			} else {
				legacyCalls++
			}
			if request.Method == "broadcaster_getQuote" {
				result = v.Quote
			} else {
				result = Fee{FeeRecipient: common.HexToAddress(v.Lock.FeeRecipient), FeeAmount: "2", MaxGas: "100000"}
			}
		default:
			t.Error("unexpected method", request.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	client, err := NewClient(Configuration{server.URL, trust.Signer, common.HexToAddress(v.Lock.FeeRecipient)}, Options{server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := client.ForPreparation(context.Background(), data, trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scoped.EstimateQuote(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if _, err = scoped.GetQuote(context.Background(), data, trust); err != nil {
		t.Fatal(err)
	}
	if _, err = client.EstimateQuote(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	advertised = false
	legacy, err := client.ForPreparation(context.Background(), data, trust)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != client || lockedCalls != 2 || legacyCalls != 1 {
		t.Fatalf("isolation/compatibility: %d %d", lockedCalls, legacyCalls)
	}

	advertised = true
	initializer, ok := any(client).(tokenSupportInitializer)
	if !ok {
		t.Fatal("initializer missing")
	}
	if supported, err := initializer.InitializeTokenSupport(context.Background(), common.HexToAddress(v.Lock.Token), trust, func(ctx context.Context) error { return ctx.Err() }); err != nil || !supported {
		t.Fatal(supported, err)
	}
	beforeStatus, beforeTariffs := statusCalls, tariffCalls
	var initialized *Client
	for i := 0; i < 2; i++ {
		initialized, err = client.ForPreparation(context.Background(), data, trust)
		if err != nil {
			t.Fatal(err)
		}
		if initialized.feeLock == nil {
			t.Fatal("initialized capability downgraded")
		}
	}
	if statusCalls != beforeStatus || tariffCalls != beforeTariffs+1 {
		t.Fatalf("status=%d tariff=%d", statusCalls-beforeStatus, tariffCalls-beforeTariffs)
	}
	// Live submission checks preserve unchanged capability and remain mandatory.
	signed := v.Quote
	signed.Context = &trust
	if _, err = initialized.SubmissionRequirements(context.Background(), signed); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ForPreparation(context.Background(), data, trust); err != nil {
		t.Fatal("unchanged live status retired initialization", err)
	}
	advertised = false
	if _, err = initialized.SubmissionRequirements(context.Background(), signed); err != nil {
		t.Fatal(err)
	}
	if _, err = initialized.EstimateQuote(context.Background(), data); err == nil {
		t.Fatal("scoped pricing client ignored shared revocation")
	}
	if _, err = client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("changed live capability downgraded")
	}
}

func TestMalformedAdvertisedFeeLockNeverDowngrades(t *testing.T) {
	for _, cap := range []string{`{"version":"2","model":"core-gas-v1.3.4","core":false,"router":false}`, `{"version":"1","model":"unknown","core":false,"router":false}`, `null`, `{"version":"1","model":"core-gas-v1.3.4","router":false}`, `{"version":"1","model":"core-gas-v1.3.4","core":null,"router":false}`, `{"version":"1","model":"core-gas-v1.3.4","core":true}`} {
		t.Run(cap, func(t *testing.T) {
			data, trust, _ := intent(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     uint64
					Method string
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method != "broadcaster_getStatus" && req.Method != "broadcaster_getTokenSupport" {
					t.Error("malformed capability continued to", req.Method)
				}
				result := map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": trust.ChainID.String(), "protocol": trust.Protocol, "sender": trust.Signer, "feeRecipient": common.HexToAddress("0x33"), "submissionHalted": false, "feeLock": json.RawMessage(cap)}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			client, err := NewClient(Configuration{server.URL, trust.Signer, common.HexToAddress("0x33")}, Options{server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.ForPreparation(context.Background(), data, trust); err == nil {
				t.Fatal("malformed advertised capability silently downgraded")
			}
			request, _ := DecodeIntent(data)
			if _, err = client.InitializeTokenSupport(context.Background(), request.Token, trust, func(ctx context.Context) error { return ctx.Err() }); err == nil {
				t.Fatal("malformed capability initialized downgrade authority")
			}

		})
	}
}
