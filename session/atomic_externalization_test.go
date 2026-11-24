package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"math/big"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

var gapInjected = errors.New("injected frozen journal persistence failure")

type gapRejectFrozenStore struct {
	WalletStore
	id string
}

func (s gapRejectFrozenStore) Update(ctx context.Context, scope WalletScope, apply func(*WalletState) error) error {
	return s.WalletStore.Update(ctx, scope, func(v *WalletState) error {
		if err := apply(v); err != nil {
			return err
		}
		var p transaction.Plan
		if err := json.Unmarshal(v.Operations[s.id].Plan, &p); err != nil {
			return err
		}
		if p.Broadcaster != nil && p.Broadcaster.Request != nil {
			return gapInjected
		}
		return nil
	})
}
func TestFrozenJournalWriteFailureLeavesCompleteUnexternalizedState(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	addr, _ := s.wallet.GetAddress()
	op, err := s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: addr, Amount: big.NewInt(30)}})})
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	client, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, _ json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			writes.Add(1)
		}
	})
	if err = client.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	op, err = authorizeForTest(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.Prove(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := s.options.Store
	before, err := base.ReadSnapshot(ctx, s.scope)
	if err != nil {
		t.Fatal(err)
	}
	s.options.Store = gapRejectFrozenStore{WalletStore: base, id: op.ID()}
	_, err = client.Submit(ctx, op)
	if !errors.Is(err, gapInjected) {
		t.Fatalf("wanted injected persistence error, got %v", err)
	}
	snapshot, err := base.ReadSnapshot(ctx, s.scope)
	if err != nil {
		t.Fatal(err)
	}
	r := snapshot.Operations[op.ID()]
	var p transaction.Plan
	if err = json.Unmarshal(r.Plan, &p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Operations[op.ID()], r) {
		t.Fatal("failed atomic update changed original operation record")
	}
	if r.Externalized || r.Status != "ready" || p.Broadcaster.Request != nil {
		t.Fatalf("failed journal changed durable state: externalized=%v status=%s request=%v", r.Externalized, r.Status, p.Broadcaster.Request)
	}
	if writes.Load() != 0 {
		t.Fatalf("unexpected relay writes before durable journal: %d", writes.Load())
	}
	s.options.Store = base
	resumed, err := s.Resume(ctx, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Submit(ctx, resumed)
	if err != nil {
		t.Fatalf("fresh resumed retry failed: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("wanted one resumed relay write, got %d", writes.Load())
	}
	for _, input := range p.Inputs {
		if snapshot.Notes[input.Commitment].ReservedBy != op.ID() {
			t.Fatal("reservation unexpectedly released")
		}
	}
	t.Logf("retained reserved inputs: %d; actual relay submit calls: %d", len(p.Inputs), writes.Load())
}

// These tests retain the actual submitted body only in process, never in artifacts.
func TestFrozenJournalRestartReplaysIdenticalPacketThenUsesKnownIDStatus(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	address, _ := s.wallet.GetAddress()
	op, err := s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}})})
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	var statusCalls int
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, raw json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			packets = append(packets, append([]byte(nil), raw...))
		}
		if method == "broadcaster_getTransactionStatus" {
			statusCalls++
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	op, err = authorizeForTest(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.Prove(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dropping, err := broadcaster.NewClient(c.Configuration(), broadcaster.Options{HTTPClient: &http.Client{Transport: dropScheduledSubmitResponse{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dropping.Submit(ctx, op); err == nil {
		t.Fatal("lost response swallowed")
	}
	resumed, err := s.Resume(ctx, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.plan.Broadcaster.Request == nil || resumed.status != "uncertain" {
		t.Fatal("restart lost frozen packet")
	}
	if _, err = c.Submit(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 2 || !bytes.Equal(packets[0], packets[1]) {
		t.Fatal("restart replay changed exact request")
	}
	resumed, err = s.Resume(ctx, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 2 || statusCalls != 1 {
		t.Fatalf("known ID resent packet: submits=%d statuses=%d", len(packets), statusCalls)
	}
}

func TestCustomExternalizationPolicyStillBlocksBroadcasterSend(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	address, _ := s.wallet.GetAddress()
	op, err := s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}})})
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, _ json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			writes.Add(1)
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	op, err = authorizeForTest(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.Prove(ctx)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("custom externalization policy denied")
	var called bool
	s.runtime.OnExternalize = func(context.Context, *transaction.Execution) error { called = true; return denied }
	if _, err = c.Submit(ctx, op); !errors.Is(err, denied) {
		t.Fatalf("custom policy lost: %v", err)
	}
	if !called || writes.Load() != 0 {
		t.Fatalf("policy called=%v relay writes=%d", called, writes.Load())
	}
	state, err := s.options.Store.ReadSnapshot(ctx, s.scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.Operations[op.ID()].Externalized {
		t.Fatal("custom rejection externalized record")
	}
}

type gapRejectAfterAtomicFreezeStore struct {
	WalletStore
	id           string
	frozenWrites int
}

func (s *gapRejectAfterAtomicFreezeStore) Update(ctx context.Context, scope WalletScope, apply func(*WalletState) error) error {
	return s.WalletStore.Update(ctx, scope, func(v *WalletState) error {
		if err := apply(v); err != nil {
			return err
		}
		r := v.Operations[s.id]
		var p transaction.Plan
		if err := json.Unmarshal(r.Plan, &p); err != nil {
			return err
		}
		if p.Broadcaster != nil && p.Broadcaster.Request != nil {
			s.frozenWrites++
			if !r.Externalized || r.Status != "uncertain" || len(r.CallData) == 0 || len(r.Proof) == 0 {
				return errors.New("atomic freeze is incomplete")
			}
			if s.frozenWrites == 2 {
				return gapInjected
			}
		}
		return nil
	})
}
func TestFrozenJournalCrashAfterAtomicCommitBeforeSendCanResume(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	address, _ := s.wallet.GetAddress()
	op, err := s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}})})
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, raw json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			packets = append(packets, append([]byte(nil), raw...))
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	op, err = authorizeForTest(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.Prove(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := s.options.Store
	interrupted := &gapRejectAfterAtomicFreezeStore{WalletStore: base, id: op.ID()}
	s.options.Store = interrupted
	if _, err = c.Submit(ctx, op); !errors.Is(err, gapInjected) {
		t.Fatalf("wanted interruption after atomic freeze: %v", err)
	}
	if len(packets) != 0 {
		t.Fatal("sent before interrupted persistence completed")
	}
	state, err := base.ReadSnapshot(ctx, s.scope)
	if err != nil {
		t.Fatal(err)
	}
	r := state.Operations[op.ID()]
	var p transaction.Plan
	if err = json.Unmarshal(r.Plan, &p); err != nil {
		t.Fatal(err)
	}
	if !r.Externalized || r.Status != "uncertain" || p.Broadcaster.Request == nil {
		t.Fatal("atomic freeze not durable")
	}
	expected, err := json.Marshal(p.Broadcaster.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range p.Inputs {
		if state.Notes[input.Commitment].ReservedBy != op.ID() {
			t.Fatal("interruption released input")
		}
	}
	s.options.Store = base
	resumed, err := s.Resume(ctx, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 {
		t.Fatalf("recovery sends=%d", len(packets))
	}
	var actual any
	if err = json.Unmarshal(packets[0], &actual); err != nil {
		t.Fatal(err)
	}
	normalized, _ := json.Marshal(actual)
	var expectedValue any
	if err = json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatal(err)
	}
	expected, _ = json.Marshal(expectedValue)
	if !bytes.Equal(expected, normalized) {
		t.Fatal("recovery changed atomically frozen packet")
	}
}

type finalNetworkProvider struct {
	sessionProvider
	changed atomic.Bool
}

func (p *finalNetworkProvider) ChainID(ctx context.Context) (*big.Int, error) {
	if p.changed.Load() {
		return big.NewInt(1), nil
	}
	return p.sessionProvider.ChainID(ctx)
}

type afterFrozenStore struct {
	WalletStore
	onFrozen func()
}

func (s afterFrozenStore) Update(ctx context.Context, scope WalletScope, change func(*WalletState) error) error {
	frozen := false
	err := s.WalletStore.Update(ctx, scope, func(state *WalletState) error {
		if e := change(state); e != nil {
			return e
		}
		for _, r := range state.Operations {
			if r.Externalized && r.Status == "uncertain" {
				frozen = true
			}
		}
		return nil
	})
	if err == nil && frozen {
		s.onFrozen()
	}
	return err
}
func TestPrivateFirstSendChecksNetworkAfterFrozenJournal(t *testing.T) {
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	addr, _ := s.wallet.GetAddress()
	op, err := s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: addr, Amount: big.NewInt(30)}})})
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, _ json.RawMessage) {
		if method == "broadcaster_submitTransaction" {
			writes.Add(1)
		}
	})
	if err = c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}); err != nil {
		t.Fatal(err)
	}
	op, err = authorizeForTest(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.Prove(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &finalNetworkProvider{sessionProvider: s.options.Provider.(sessionProvider)}
	op.session.options.Provider = p
	s.options.Store = afterFrozenStore{s.options.Store, func() { p.changed.Store(true) }}
	if _, err = c.Submit(ctx, op); err == nil || writes.Load() != 0 {
		t.Fatalf("send crossed changed network: error=%v writes=%d", err, writes.Load())
	}
	if op.status != "uncertain" || op.plan.Broadcaster.Request == nil {
		t.Fatal("lost frozen attempt after send guard")
	}
	if _, err = c.Submit(ctx, op); err != nil || writes.Load() != 1 {
		t.Fatal("unknown-id recovery failed", err)
	}
}
