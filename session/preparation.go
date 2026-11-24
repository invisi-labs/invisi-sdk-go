package session

import (
	"context"
	"errors"

	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"

	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

func (s *WalletSession) PrepareTransfer(ctx context.Context, p sdk.TransferPreparation) (*Operation, error) {
	return s.prepareWithSnapshot(ctx, func(r *transaction.Runtime) (*transaction.Execution, error) { return r.PrepareTransfer(ctx, p) })
}
func (s *WalletSession) PrepareShield(ctx context.Context, p sdk.ShieldPreparation) (*Operation, error) {
	var anchor *shieldPreparationAnchor
	if s.shieldPreparations != nil {
		anchor = s.shieldPreparations.Load()
	}
	r := s.runtimeSnapshot()
	if anchor == nil || r.CanonicalShieldBinding(p) == nil {
		return s.prepareWithSnapshot(ctx, func(r *transaction.Runtime) (*transaction.Execution, error) { return r.PrepareShield(ctx, p) })
	}
	if _, ok := anchor.tokens[p.TokenAddress]; ok {
		if err := anchor.checkWallet(ctx, s); err != nil {
			s.shieldPreparations.CompareAndSwap(anchor, &shieldPreparationAnchor{})
			return nil, err
		}
		r.ShieldPreparation = &transaction.ShieldPreparationReceipt{Block: anchor.block, Token: p.TokenAddress, Check: func(ctx context.Context, r *transaction.Runtime) error {
			if anchor.binding != r.Options.Provider || anchor.protocol != r.Protocol || anchor.rpc != r.RPC || anchor.scope != r.Scope {
				return errors.New("Shield capture connections changed")
			}
			return anchor.checkWallet(ctx, s)
		}}
		r.OnComplete = func(ctx context.Context, e *transaction.Execution) error {
			return s.reserveGuardedExecution(ctx, e, anchor)
		}
	}
	// Missing tokens and failed captures go through the live native frame.
	e, err := r.PrepareShield(ctx, p)
	if err != nil {
		return nil, err
	}
	return operationFromExecution(s, e), nil
}
