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
		case "broadcaster_getStatus":
			status := ServiceStatus{APIVersion: "v1", ProtocolVersion: "1", ChainID: trust.ChainID.String(), Protocol: trust.Protocol, Sender: trust.Signer, FeeRecipient: common.HexToAddress(v.Lock.FeeRecipient)}
			if advertised {
				status.FeeLock = &FeeLockCapabilities{Version: "1", Model: feeLockModel, Core: true}
			}
			result = status
		case "broadcaster_lockFee":
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
}

func TestMalformedAdvertisedFeeLockNeverDowngrades(t *testing.T) {
	for _, cap := range []string{`null`, `{"version":"1","model":"core-gas-v1.3.4","router":false}`, `{"version":"1","model":"core-gas-v1.3.4","core":null,"router":false}`, `{"version":"1","model":"core-gas-v1.3.4","core":true}`} {
		t.Run(cap, func(t *testing.T) {
			data, trust, _ := intent(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     uint64
					Method string
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method != "broadcaster_getStatus" {
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
		})
	}
}
