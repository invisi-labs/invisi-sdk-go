package session

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"
)

func TestPlainShieldQuoteUsesPaidEstimateThenExactSignedQuote(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	address, _ := s.wallet.GetAddress()
	op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, raw json.RawMessage) {
		if method != "broadcaster_estimateQuote" && method != "broadcaster_getQuote" {
			return
		}
		methods = append(methods, method)
		var p struct{ Data string }
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Error(err)
			return
		}
		decoded, err := broadcaster.DecodeIntent(common.FromHex(p.Data))
		if err != nil {
			t.Error(err)
			return
		}
		if method == "broadcaster_estimateQuote" && decoded.FeeAmount.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("initial estimate lacks paid features: fee=%s", decoded.FeeAmount)
		}
		if method == "broadcaster_getQuote" && decoded.FeeAmount.Cmp(big.NewInt(5)) != 0 {
			t.Errorf("signed quote does not bind exact fee: %s", decoded.FeeAmount)
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "broadcaster_estimateQuote" || methods[1] != "broadcaster_getQuote" {
		t.Fatalf("plain quote methods=%v", methods)
	}
}

type quoteRefusalTransport struct {
	base      http.RoundTripper
	remaining int
	requests  []string
}

func (tr *quoteRefusalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req struct {
		ID     uint64
		Method string
	}
	json.Unmarshal(raw, &req)
	tr.requests = append(tr.requests, req.Method)
	if req.Method == "broadcaster_getQuote" && tr.remaining > 0 {
		tr.remaining--
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "quote refused"}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}, Request: r}, nil
	}
	return tr.base.RoundTrip(r)
}
func TestPlainShieldQuoteRefreshRequiresRealIncreaseWithinCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fees    []int64
		want    int64
		wantErr bool
	}{
		{"increase", []int64{5, 7}, 7, false}, {"same refusal", []int64{5, 5}, 0, true}, {"over cap", []int64{5, 11}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, token, _ := testSession(t, uint64(time.Now().Unix()))
			address, _ := s.wallet.GetAddress()
			op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
			if err != nil {
				t.Fatal(err)
			}
			original, _ := testBroadcaster(t, op, tc.fees, false)
			tr := &quoteRefusalTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, remaining: 1}
			c, err := broadcaster.NewClient(original.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)})
			if (err != nil) != tc.wantErr {
				t.Fatalf("quote err=%v", err)
			}
			if !tc.wantErr && op.Preview().FeeAmount.Int64() != tc.want {
				t.Fatal("fee increase not bound")
			}
			estimates, quotes := 0, 0
			for _, method := range tr.requests {
				if method == "broadcaster_estimateQuote" {
					estimates++
				}
				if method == "broadcaster_getQuote" {
					quotes++
				}
			}
			expectedQuotes := 1
			if !tc.wantErr {
				expectedQuotes = 2
			}
			if estimates != 2 || quotes != expectedQuotes {
				t.Fatalf("unexpected refresh methods %v", tr.requests)
			}
		})
	}
}
