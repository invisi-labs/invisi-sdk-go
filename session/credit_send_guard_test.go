package session

import (
	"context"
	"testing"
	"time"

	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	creditsession "github.com/invisi-labs/invisi-sdk-go/session/credit"
)

type creditFenceSnapshotStore struct {
	WalletStore
	duringRead func(*WalletState)
}

func (s creditFenceSnapshotStore) ReadSnapshot(ctx context.Context, scope WalletScope) (WalletState, error) {
	state, err := s.WalletStore.ReadSnapshot(ctx, scope)
	if err == nil {
		s.duringRead(&state)
	}
	return state, err
}

func TestComposedCreditFenceChecksAuthorityAcrossWalletRead(t *testing.T) {
	for _, change := range []string{"none", "native-epoch", "eventless-network", "worker", "wallet-generation"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			wallet, _, _ := testSession(t, uint64(time.Now().Unix()))
			mock := wallet.options.Provider.(sessionProvider)
			current := &finalNetworkProvider{sessionProvider: mock}
			wallet.options.Provider = current
			var binding *chain.HTTPBinding
			if change == "native-epoch" {
				url := auditRPCEndpoint(t, &submissionProvider{sessionProvider: mock}, &submitHTTPAudit{methods: map[string]int{}})
				var err error
				binding, err = chain.NewHTTPBinding(ctx, url)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(binding.Close)
				wallet.options.Provider = binding
			}
			const id, worker = "credit", "owner"
			if err := wallet.options.Store.Update(ctx, wallet.scope, func(s *WalletState) error {
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
			wallet.options.Store = creditFenceSnapshotStore{wallet.options.Store, func(state *WalletState) {
				switch change {
				case "native-epoch":
					binding.Invalidate()
				case "eventless-network":
					current.changed.Store(true)
				case "worker":
					r := state.PostpaidCredit.Operations[id]
					r.WorkerID = "new"
					state.PostpaidCredit.Operations[id] = r
				case "wallet-generation":
					state.Scan.Generation++
				}
			}}
			err = guard(ctx)
			if (err == nil) != (change == "none") {
				t.Fatalf("unexpected guard result %s: %v", change, err)
			}
		})
	}
}
