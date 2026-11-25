package shared

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
)

// GasEstimator is the host capability required before direct submission.
type GasEstimator interface {
	EstimateGas(context.Context, ethereum.CallMsg) (uint64, error)
}

// SubmissionOptions budgets the exact frozen call using a live independent simulation.
func SubmissionOptions(ctx context.Context, provider GasEstimator, owner common.Address, call sdk.TransactionCall) (walletsession.TransactionOptions, error) {
	gas, err := provider.EstimateGas(ctx, ethereum.CallMsg{From: owner, To: &call.To, Data: call.Data, Value: call.Value})
	if err != nil {
		return walletsession.TransactionOptions{}, err
	}
	return walletsession.TransactionOptions{GasLimit: gas + gas/5}, nil
}
