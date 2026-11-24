package session

import (
	"context"
	"errors"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"

	"github.com/invisi-labs/invisi-sdk-go/internal/storewatch"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	creditsession "github.com/invisi-labs/invisi-sdk-go/session/credit"
)

// Credit composes independent grant state and journals with this wallet's encrypted Store.
func (s *WalletSession) Credit() (*creditsession.Session, error) {
	if s == nil || s.options.Store == nil {
		return nil, errors.New("managed wallet store required")
	}
	coreContext := s.coreContext()
	return creditsession.New(creditsession.Options{CaptureSendGuard: s.captureCreditSendGuard, ChainTimestamp: func(ctx context.Context) (uint64, error) {
		if s.options.Provider == nil {
			return 0, errors.New("current Credit chain provider required")
		}
		head, err := s.options.Provider.HeaderByNumber(ctx, nil)
		if err != nil {
			return 0, err
		}
		if head == nil || head.Number == nil || !head.Number.IsUint64() || head.Time == 0 {
			return 0, errors.New("current Credit block unavailable")
		}
		return head.Time, nil
	}, WrapPreparation: transaction.WrapPreparation, Build: s.buildCreditRequest, Store: creditStoreAdapter{session: s}, OwnerAddress: s.scope.OwnerAddress, Context: &coreContext, Validate: s.validateCreditIntent, CheckCancellation: s.checkCreditCancellation, Prepare: s.prepareCreditIntent, ValidatePreparation: s.validateCreditPreparation, CheckPreparation: s.checkCreditPreparation, VerifyRevocation: s.verifyCreditRevocation})
}

func (s *WalletSession) captureCreditSendGuard(ctx context.Context, id, worker string) (func(context.Context) error, error) {
	state, err := s.fencedSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if state.Invalidated {
		return nil, errors.New("wallet history is invalidated")
	}
	generation := state.Scan.Generation
	checkNetwork := s.protocol.CaptureSendNetwork(func() protocolrpc.ChainProvider { return s.options.Provider })
	binding, _ := s.options.Provider.(*chain.HTTPBinding)
	var epoch uint64
	if binding != nil {
		epoch = binding.Epoch()
	}
	check := func(ctx context.Context) error {
		// An owned HTTP binding has a finite epoch we can compare after the
		// wallet store returns. An eventless provider needs the real read last.
		if binding != nil {
			if err := checkNetwork(ctx); err != nil {
				return err
			}
		}
		current, err := s.fencedSnapshot(ctx)
		if err != nil {
			return err
		}
		record, ok := current.PostpaidCredit.Operations[id]
		if !ok || record.WorkerID != worker || current.Invalidated || current.Scan.Generation != generation {
			return errors.New("Credit wallet authority changed after freezing the request")
		}
		if binding != nil {
			if s.options.Provider != binding || binding.Epoch() != epoch {
				return errors.New("Credit provider changed after freezing the request")
			}
			return ctx.Err()
		}
		return checkNetwork(ctx)
	}
	return func(ctx context.Context) error {
		finish := s.runtime.ObserveSendWrites()
		return storewatch.Check(finish, func() error { return check(ctx) })
	}, nil
}

type creditStoreAdapter struct{ session *WalletSession }

func (a creditStoreAdapter) ObserveScopeWrites(string) func() bool {
	return a.session.runtime.ObserveSendWrites()
}

func (a creditStoreAdapter) ReadSnapshot(ctx context.Context) (creditsession.State, error) {
	state, err := a.session.fencedSnapshot(ctx)
	if err != nil {
		return creditsession.State{}, err
	}
	if state.Invalidated {
		return creditsession.State{}, errors.New("wallet requires reset scan")
	}
	if state.PostpaidCredit.Version == 0 {
		return creditsession.EmptyState(), nil
	}
	if state.PostpaidCredit.Version != 2 {
		return creditsession.State{}, errors.New("legacy Credit store is unsupported")
	}
	return creditsession.CloneState(state.PostpaidCredit)
}
func (a creditStoreAdapter) Update(ctx context.Context, change func(*creditsession.State) error) error {
	var generation *uint64
	if a.session.publicHistory != nil {
		public, err := a.session.publicHistory.Snapshot(ctx)
		if err != nil {
			return err
		}
		generation = &public.Generation
	}
	err := a.session.options.Store.Update(ctx, a.session.scope, func(state *WalletState) error {
		if state.Invalidated || generation != nil && state.Scan.Generation != *generation {
			return errors.New("wallet requires reset scan")
		}
		next := state.PostpaidCredit
		if next.Version == 0 {
			next = creditsession.EmptyState()
		}
		if next.Version != 2 {
			return errors.New("legacy Credit store is unsupported")
		}
		if next.Revision == ^uint64(0) {
			return errors.New("Credit revision exhausted")
		}
		if err := change(&next); err != nil {
			return err
		}
		next.Revision++
		detached, err := creditsession.CloneState(next)
		if err != nil {
			return err
		}
		state.PostpaidCredit = detached
		return nil
	})
	if err != nil {
		return err
	}
	if generation == nil {
		return nil
	}
	after, err := a.session.publicHistory.Snapshot(ctx)
	if err != nil {
		return err
	}
	if after.Generation != *generation {
		_ = a.session.invalidate(ctx)
		return errors.New("wallet requires reset scan")
	}
	return nil
}
