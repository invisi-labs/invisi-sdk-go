package session

import (
	"context"
	"encoding/json"
	"errors"

	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
)

func (s *WalletSession) reserveExecution(ctx context.Context, execution *transaction.Execution) error {
	return s.reserveGuardedExecution(ctx, execution, nil)
}
func (s *WalletSession) reserveGuardedExecution(ctx context.Context, execution *transaction.Execution, anchor *shieldPreparationAnchor) error {
	plan := execution.Plan
	cached := execution.Runtime.Snapshot
	if cached != nil && s.publicHistory != nil {
		public, err := s.publicHistory.Snapshot(ctx)
		if err != nil {
			return err
		}
		if public.Generation != cached.Generation {
			return errors.New("wallet requires reset refresh")
		}
	}
	id := plan.ID
	encoded, e := json.Marshal(plan)
	if e != nil {
		return e
	}
	e = s.options.Store.Update(ctx, s.scope, func(state *WalletState) error {
		if anchor != nil {
			if err := anchor.checkState(state); err != nil {
				return err
			}
			if err := anchor.current(ctx, s); err != nil {
				return err
			}
		}
		if state.Invalidated {
			return errors.New("wallet requires reset refresh")
		}
		for _, n := range plan.Inputs {
			current, ok := state.Notes[n.Commitment]
			if !ok || current.Spent || current.ReservedBy != "" {
				return errors.New("input already spent or reserved")
			}
			if !current.Note.Equal(n.Note) || current.TreeID != n.TreeID || current.LeafIndex != n.LeafIndex {
				return errors.New("input changed during preparation")
			}
		}

		record := OperationRecord{ID: id, Kind: transaction.PublicOperationKind(plan), Status: "prepared", Deadline: plan.Deadline, Plan: encoded, WorkerID: id}
		for _, n := range plan.Inputs {
			current := state.Notes[n.Commitment]
			current.ReservedBy = id
			state.Notes[n.Commitment] = current
			record.Inputs = append(record.Inputs, n.Commitment)
		}

		state.Operations[id] = record
		return nil
	})
	if e == nil && anchor != nil {
		// Public history can share a single SQLite connection with the wallet.
		// Fence it outside the wallet transaction, then revoke only this
		// unexternalized preparation if the public generation or ticket changed.
		if err := anchor.checkWallet(ctx, s); err != nil {
			cleanup := s.options.Store.Update(context.WithoutCancel(ctx), s.scope, func(v *WalletState) error {
				record, ok := v.Operations[id]
				if ok && !record.Externalized && record.WorkerID == id && record.Status == "prepared" {
					delete(v.Operations, id)
				}
				return nil
			})
			return errors.Join(err, cleanup)
		}
	}
	if e == nil && cached != nil && s.publicHistory != nil {
		public, err := s.publicHistory.Snapshot(ctx)
		if err != nil {
			return err
		}
		if public.Generation != cached.Generation {
			_ = s.invalidate(ctx)
			return errors.New("wallet requires reset refresh")
		}
	}
	return e
}
