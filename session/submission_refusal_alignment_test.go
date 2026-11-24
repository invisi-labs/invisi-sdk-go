package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"
)

type structuredRefusalTransport struct {
	base   http.RoundTripper
	writes int
	code   int
	reason string
}

func (tr *structuredRefusalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
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
	if req.Method != "broadcaster_submitTransaction" {
		return tr.base.RoundTrip(r)
	}
	tr.writes++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": tr.code, "message": tr.reason}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}, Request: r}, nil
}
func TestOrdinaryStructuredFirstRefusalRollsBackButAmbiguityStaysFrozen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		reason   string
		rollback bool
	}{
		{"slack", -32025, "INSUFFICIENT_EXECUTION_SLACK: refresh required", true},
		{"period", -32024, "CREDIT_PERIOD_CHANGED", true},
		{"ending", -32026, "CREDIT_PERIOD_ENDING", true},
		{"wrong reason", -32025, "unspecified refusal", false},
		{"generic", -32000, "INSUFFICIENT_EXECUTION_SLACK", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, token, _ := testSession(t, uint64(time.Now().Unix()))
			address, _ := s.wallet.GetAddress()
			op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := testBroadcaster(t, op, []int64{5}, false)
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
			tr := &structuredRefusalTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, code: tc.code, reason: tc.reason}
			refusing, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = refusing.Submit(ctx, op); err == nil {
				t.Fatal("refusal swallowed")
			}
			state, err := s.options.Store.ReadSnapshot(ctx, s.scope)
			if err != nil {
				t.Fatal(err)
			}
			r := state.Operations[op.ID()]
			if r.Externalized == tc.rollback {
				t.Fatalf("externalized=%v rollback=%v", r.Externalized, tc.rollback)
			}
			resumed, err := s.Resume(ctx, op.ID())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = refusing.Submit(ctx, resumed); err == nil {
				t.Fatal("repeat refusal swallowed")
			}
			want := 2
			if tc.rollback {
				want = 1
			}
			if tr.writes != want {
				t.Fatalf("refusal replay writes=%d want=%d", tr.writes, want)
			}
		})
	}
}

type rejectRefusalJournalStore struct{ WalletStore }

func (s rejectRefusalJournalStore) Update(ctx context.Context, scope WalletScope, apply func(*WalletState) error) error {
	return s.WalletStore.Update(ctx, scope, func(state *WalletState) error {
		if err := apply(state); err != nil {
			return err
		}
		for _, r := range state.Operations {
			var p transaction.Plan
			if err := json.Unmarshal(r.Plan, &p); err != nil {
				return err
			}
			if p.Broadcaster != nil && p.Broadcaster.SubmissionRefusal != "" {
				return gapInjected
			}
		}
		return nil
	})
}
func TestStructuredRefusalRollbackFailureKeepsRecoverableFrozenJournal(t *testing.T) {
	op, c, _, _, owner := ownedNativeShieldFixture(t)
	ctx := context.Background()
	tr := &structuredRefusalTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, code: -32025, reason: "INSUFFICIENT_EXECUTION_SLACK"}
	refusing, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	base := owner.options.Store
	owner.options.Store = rejectRefusalJournalStore{base}
	if _, err = refusing.Submit(ctx, op); !errors.Is(err, gapInjected) {
		t.Fatalf("rollback persistence failure lost: %v", err)
	}
	if op.Status() != "uncertain" || op.plan.Broadcaster.Request == nil || op.plan.Broadcaster.SubmissionRefusal != "" {
		t.Fatal("failed rollback changed in-memory frozen state")
	}
	state, err := base.ReadSnapshot(ctx, op.session.scope)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Operations[op.ID()].Externalized {
		t.Fatal("failed rollback released externalization")
	}
	owner.options.Store = base
	resumed, err := op.session.Resume(ctx, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = refusing.Submit(ctx, resumed); err == nil {
		t.Fatal("replayed structured refusal swallowed")
	}
	state, err = base.ReadSnapshot(ctx, op.session.scope)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Operations[op.ID()].Externalized || resumed.plan.Broadcaster.Request == nil {
		t.Fatal("replay refusal rolled back earlier unknown attempt")
	}
}

func TestStructuredFirstRelayRefusalNeverRollsBackPreviouslyExportedAuthority(t *testing.T) {
	op, c, _, _, owner := ownedNativeShieldFixture(t)
	ctx := context.Background()
	if _, err := op.ExportTransaction(ctx); err != nil {
		t.Fatal(err)
	}
	tr := &structuredRefusalTransport{base: &http.Transport{TLSClientConfig: auditTLSConfig()}, code: -32025, reason: "INSUFFICIENT_EXECUTION_SLACK"}
	refusing, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = refusing.Submit(ctx, op); err == nil {
		t.Fatal("refusal swallowed")
	}
	state, err := owner.options.Store.ReadSnapshot(ctx, owner.scope)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Operations[op.ID()].Externalized || op.plan.Broadcaster.Request == nil || op.plan.Broadcaster.SubmissionRefusal != "" {
		t.Fatal("relay refusal rolled back already exported authority")
	}
}
