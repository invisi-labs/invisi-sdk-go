package session

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	creditsession "github.com/invisi-labs/invisi-sdk-go/session/credit"
)

type sendHookProvider struct {
	sessionProvider
	onRead func()
}

func (p *sendHookProvider) ChainID(ctx context.Context) (*big.Int, error) {
	if change := p.onRead; change != nil {
		p.onRead = nil
		change()
	}
	return p.sessionProvider.ChainID(ctx)
}

func TestComposedCreditDetectsWritesDuringFinalNetworkRead(t *testing.T) {
	for _, mode := range []string{"unchanged", "worker", "scan-generation", "invalidated", "public-generation", "provider"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			wallet, _, _ := testSession(t, uint64(time.Now().Unix()))
			p := &sendHookProvider{sessionProvider: wallet.options.Provider.(sessionProvider)}
			wallet.options.Provider = p
			const id, worker = "credit", "owner"
			raw := wallet.options.Store.(*notifyingWalletStore).base
			if err := raw.Update(ctx, wallet.scope, func(s *WalletState) error {
				s.PostpaidCredit = creditsession.EmptyState()
				s.PostpaidCredit.Operations[id] = creditsession.Journal{WorkerID: worker}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			guard, err := wallet.captureCreditSendGuard(ctx, id, worker)
			if err != nil {
				t.Fatal(err)
			}
			p.onRead = func() {
				switch mode {
				case "unchanged":
					return
				case "provider":
					wallet.options.Provider = &sendHookProvider{sessionProvider: p.sessionProvider}
					return
				case "public-generation":
					err = wallet.publicHistory.Store().Update(ctx, wallet.publicHistory.Key(), func(s *PublicHistoryState) error { s.Generation++; return nil })
				default:
					err = raw.Update(ctx, wallet.scope, func(s *WalletState) error {
						switch mode {
						case "worker":
							r := s.PostpaidCredit.Operations[id]
							r.WorkerID = "new-worker"
							s.PostpaidCredit.Operations[id] = r
						case "scan-generation":
							s.Scan.Generation++
						case "invalidated":
							s.Invalidated = true
						}
						return nil
					})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = guard(ctx); (err == nil) != (mode == "unchanged") {
				t.Fatalf("guard %s: %v", mode, err)
			}
		})
	}
}

func TestNoteFirstSendObservesWritesDuringFinalNetworkRead(t *testing.T) {
	for _, mode := range []string{"unchanged", "worker", "invalidated", "provider"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			wallet, token, _ := testSession(t, uint64(time.Now().Unix()))
			recipient, _ := wallet.wallet.GetAddress()
			op, err := wallet.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(wallet, []testPaymentOutput{{To: recipient, Amount: big.NewInt(30)}})})
			if err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int64
			var firstPacket broadcaster.Submission
			c, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", func(method string, request json.RawMessage) {
				if method == "broadcaster_submitTransaction" {
					if err := json.Unmarshal(request, &firstPacket); err != nil {
						t.Error(err)
					}
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
			p := &sendHookProvider{sessionProvider: op.session.options.Provider.(sessionProvider)}
			op.session.options.Provider = p
			raw := wallet.options.Store.(*notifyingWalletStore).base
			wallet.options.Store = afterFrozenStore{wallet.options.Store, func() {
				p.onRead = func() {
					if mode == "unchanged" {
						return
					}
					if mode == "provider" {
						op.session.options.Provider = &sendHookProvider{sessionProvider: p.sessionProvider}
						return
					}
					if err := raw.Update(ctx, wallet.scope, func(s *WalletState) error {
						if mode == "invalidated" {
							s.Invalidated = true
						} else {
							r := s.Operations[op.plan.ID]
							r.WorkerID = "replacement"
							s.Operations[op.plan.ID] = r
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}}
			_, err = c.Submit(ctx, op)
			if mode == "unchanged" {
				if err != nil || writes.Load() != 1 {
					t.Fatalf("control: %v writes %d", err, writes.Load())
				}
				return
			}
			if err == nil || writes.Load() != 0 {
				t.Fatalf("first send: error %v writes %d", err, writes.Load())
			}
			st, err := raw.ReadSnapshot(ctx, wallet.scope)
			if err != nil {
				t.Fatal(err)
			}
			r := st.Operations[op.plan.ID]
			var frozen struct {
				Broadcaster *struct{ Request *broadcaster.Submission }
			}
			if err = json.Unmarshal(r.Plan, &frozen); err != nil {
				t.Fatal(err)
			}
			if !r.Externalized || r.Status != "uncertain" || frozen.Broadcaster == nil || frozen.Broadcaster.Request == nil || st.Notes[op.plan.Inputs[0].Commitment].ReservedBy != op.plan.ID {
				t.Fatal("lost frozen request or input reservation")
			}
			// Simulate successful wallet-history recovery before the worker reclaims its record.
			if mode == "invalidated" {
				if err := raw.Update(ctx, wallet.scope, func(s *WalletState) error { s.Invalidated = false; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			op, err = wallet.Resume(ctx, op.plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Submit(ctx, op); err != nil || writes.Load() != 1 || !reflect.DeepEqual(&firstPacket, frozen.Broadcaster.Request) {
				t.Fatalf("replay differs from durable request: %v writes %d", err, writes.Load())
			}
		})
	}
}

// A bare custom adapter can retain maps or functions in an uncomparable value.
type customWalletValue struct {
	WalletStore
	labels map[string]string
}

func TestSessionsObserveSharedCustomWalletWrites(t *testing.T) {
	ctx := context.Background()
	first, _, _ := testSession(t, uint64(time.Now().Unix()))
	custom := customWalletValue{WalletStore: first.options.Store.(*notifyingWalletStore).base, labels: map[string]string{}}
	options := Options{Invisi: sdk.NewInvisi(first.options.InvisiOptions), Wallet: first.wallet, Protocol: first.protocol, Namespace: first.scope.Namespace, Store: custom, PublicHistory: first.publicHistory}
	a, err := Connect(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Connect(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	ctxWrite, cancel := context.WithCancel(ctx)
	defer cancel()
	began, release := make(chan struct{}), make(chan struct{})
	wrote := make(chan error, 1)
	go func() {
		wrote <- b.options.Store.Update(ctxWrite, b.scope, func(st *WalletState) error { close(began); <-release; return ctxWrite.Err() })
	}()
	<-began
	finish := a.runtime.ObserveSendWrites()
	cancel()
	close(release)
	if err := <-wrote; err == nil {
		t.Fatal("expected aborted write")
	}
	if !finish() {
		t.Fatal("shared bare adapter lost an in-flight SDK write")
	}
	finish = a.runtime.ObserveSendWrites()
	if finish() {
		t.Fatal("a previous write invalidated a later observer")
	}
}
