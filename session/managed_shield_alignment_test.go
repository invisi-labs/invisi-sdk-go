package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func ownedNativeShieldFixture(t *testing.T) (*Operation, *broadcaster.Client, *submitHTTPAudit, *submissionProvider, *WalletSession) {
	return ownedNativeShieldFixtureMode(t, true)
}
func ownedNativeShieldFixtureMode(t *testing.T, attach bool) (*Operation, *broadcaster.Client, *submitHTTPAudit, *submissionProvider, *WalletSession) {
	t.Helper()
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	fixture := &submissionProvider{sessionProvider: s.options.Provider.(sessionProvider)}
	audit := &submitHTTPAudit{methods: map[string]int{}}
	binding, err := chain.NewHTTPBinding(ctx, auditRPCEndpoint(t, fixture, audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(binding.Close)
	s.options.Provider = binding
	s.rpc = s.rpc.WithProvider(binding)
	if err = s.protocol.ValidateDeployment(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	address, _ := s.wallet.GetAddress()
	op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testBroadcaster(t, op, []int64{5}, false)
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	if !attach {
		return op, c, audit, fixture, s
	}
	request, err := op.ExportFundingAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.WithFundingSignature(ctx, sig)
	if err != nil {
		t.Fatal(err)
	}
	return op, c, audit, fixture, s
}
func TestOwnedNativeManagedShieldDelegatesExactSimulationWithTwoNetworkFences(t *testing.T) {
	op, c, audit, _, _ := ownedNativeShieldFixture(t)
	audit.reset()
	if _, err := c.Submit(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if audit.requests != 2 || audit.methods["eth_chainId"] != 2 {
		t.Fatalf("managed Shield repeated dynamic validation: %v", audit.methods)
	}
}

func TestOwnedNativeManagedShieldPreservesCustomVerifierPolicy(t *testing.T) {
	op, c, _, _, _ := ownedNativeShieldFixture(t)
	policy := &auditFundingPolicy{valid: false}
	op.session.options.FundingVerifier = policy
	if _, err := c.Submit(context.Background(), op); err == nil || !strings.Contains(err.Error(), "invalid current funding") {
		t.Fatalf("custom verifier was bypassed: %v", err)
	}
	if policy.calls != 1 {
		t.Fatalf("custom verifier calls=%d", policy.calls)
	}
	if op.Status() != "ready" {
		t.Fatal("custom policy rejection externalized operation")
	}
}

func TestStockBroadcasterDenyTransportRunsAfterCompleteAtomicFreeze(t *testing.T) {
	op, c, audit, _, _ := ownedNativeShieldFixture(t)
	denied := errors.New("local write denied before HTTP")
	tr := &managedDenyTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, deny: func() error {
		state, err := op.session.options.Store.ReadSnapshot(context.Background(), op.session.scope)
		if err != nil {
			return err
		}
		r := state.Operations[op.ID()]
		var p transaction.Plan
		if err = json.Unmarshal(r.Plan, &p); err != nil {
			return err
		}
		if !r.Externalized || r.Status != "uncertain" || p.Broadcaster.Request == nil || len(r.CallData) == 0 {
			t.Error("custom deny transport ran before complete atomic freeze")
		}
		return denied
	}}
	guarded, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	audit.reset()
	if _, err = guarded.Submit(context.Background(), op); !errors.Is(err, denied) {
		t.Fatalf("deny transport policy lost: %v", err)
	}
	if tr.denied != 1 || audit.requests != 2 || audit.methods["eth_chainId"] != 2 {
		t.Fatalf("stock transport wrapper changed managed checks: denied=%d reads=%v", tr.denied, audit.methods)
	}
	if op.Status() != "uncertain" || op.plan.Broadcaster.Request == nil {
		t.Fatal("denied unknown response released frozen packet")
	}
}

type managedDenyTransport struct {
	base   http.RoundTripper
	deny   func() error
	denied int
}

func (tr *managedDenyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req struct{ Method string }
	json.Unmarshal(raw, &req)
	if req.Method == "broadcaster_submitTransaction" {
		tr.denied++
		return nil, tr.deny()
	}
	return tr.base.RoundTrip(r)
}

func ownedUncachedQuotedShieldFixture(t *testing.T) (*Operation, *broadcaster.Client, *submitHTTPAudit, *submissionProvider, *WalletSession) {
	t.Helper()
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	fixture := &submissionProvider{sessionProvider: s.options.Provider.(sessionProvider)}
	audit := &submitHTTPAudit{methods: map[string]int{}}
	binding, err := chain.NewHTTPBinding(ctx, auditRPCEndpoint(t, fixture, audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(binding.Close)
	s.options.Provider = binding
	s.rpc = s.rpc.WithProvider(binding)
	if err = s.protocol.ValidateDeployment(ctx, binding); err != nil {
		t.Fatal(err)
	}
	address, _ := s.wallet.GetAddress()
	nonce, err := s.protocol.ReadAuthorizationNonce(ctx, binding, s.wallet.OwnerAddress())
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30), SigNonce: nonce}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testBroadcaster(t, op, []int64{5}, false)
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	return op, c, audit, fixture, s
}
func TestOwnedQuotedShieldEOAFundingAuthorizationAndAttachNeedNoRPC(t *testing.T) {
	op, _, audit, _, _ := ownedUncachedQuotedShieldFixture(t)
	audit.reset()
	request, err := op.ExportFundingAuthorization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.WithFundingSignature(context.Background(), sig); err != nil {
		t.Fatal(err)
	}
	if audit.requests != 0 {
		t.Fatalf("selected nonce was reread during local EOA authorization: %v", audit.methods)
	}
}

func TestCachedEOAAttachFundingStillHonorsExplicitCustomVerifier(t *testing.T) {
	ctx := context.Background()
	op, _, _, _, _ := ownedNativeShieldFixtureMode(t, false)
	policy := &auditFundingPolicy{valid: false}
	op.session.options.FundingVerifier = policy
	req, err := op.ExportFundingAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.WithFundingSignature(ctx, sig); err == nil {
		t.Fatal("explicit funding verifier was bypassed for recovered EOA signature")
	}
	if policy.calls != 1 {
		t.Fatalf("explicit verifier calls=%d", policy.calls)
	}
}

func TestQuotedFundingRetiredNativeLifetimeRequiresReprepare(t *testing.T) {
	for _, mode := range []string{"invalidated", "rebound"} {
		t.Run(mode, func(t *testing.T) {
			op, _, audit, fixture, _ := ownedUncachedQuotedShieldFixture(t)
			binding := op.session.options.Provider.(*chain.HTTPBinding)
			if mode == "invalidated" {
				binding.Invalidate()
			} else {
				replacement, err := chain.NewHTTPBinding(context.Background(), auditRPCEndpoint(t, fixture, audit))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(replacement.Close)
				op.session.options.Provider = replacement
			}
			audit.reset()
			if _, err := op.ExportFundingAuthorization(context.Background()); err == nil || !strings.Contains(err.Error(), "binding changed") {
				t.Fatalf("retired quoted lifetime remained signable: %v", err)
			}
			if audit.requests != 0 {
				t.Fatalf("retired lifetime silently refreshed signing state: %v", audit.methods)
			}
		})
	}
}
func TestJSONResumedUncachedQuoteKeepsConservativeFundingReads(t *testing.T) {
	op, _, audit, _, owner := ownedUncachedQuotedShieldFixture(t)
	resumed, err := owner.Resume(context.Background(), op.ID())
	if err != nil {
		t.Fatal(err)
	}
	audit.reset()
	req, err := resumed.ExportFundingAuthorization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resumed.WithFundingSignature(context.Background(), sig); err != nil {
		t.Fatal(err)
	}
	if audit.methods["eth_call"] < 1 || audit.methods["eth_getBlockByNumber"] < 1 || audit.methods["eth_chainId"] != 1 {
		t.Fatalf("JSON resume unexpectedly retained local quote lifetime: %v", audit.methods)
	}
}
