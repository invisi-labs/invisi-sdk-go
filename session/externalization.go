package session

import (
	"context"
	"encoding/json"
	"errors"

	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
)

func (s *WalletSession) externalize(ctx context.Context, e *transaction.Execution) error {
	// The externalization marker and exact recovery packet form one durable record.
	// A failed update must leave the previous, wholly unexternalized record intact.
	encoded, err := json.Marshal(e.Plan)
	if err != nil {
		return err
	}
	var proof json.RawMessage
	if e.Bundle != nil {
		proof, err = json.Marshal(e.Bundle)
		if err != nil {
			return err
		}
	}
	first := false
	err = s.options.Store.Update(ctx, s.scope, func(state *WalletState) error {
		r, ok := state.Operations[e.Plan.ID]
		if !ok || r.WorkerID != e.WorkerID || state.Invalidated {
			return errors.New("stale operation worker")
		}
		first = !r.Externalized
		r.Plan = encoded
		r.CallData = append([]byte(nil), e.Data...)
		r.TransactionHash = e.TransactionHash
		if proof != nil {
			r.Proof = proof
		}
		r.Externalized = true
		r.Status = "uncertain"
		state.Operations[e.Plan.ID] = r
		return nil
	})
	if err == nil {
		e.FirstExternalization = first
	}
	return err
}
