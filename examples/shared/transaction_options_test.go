package shared

import (
	"context"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/backends"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"math/big"
	"testing"
)

func TestDirectSubmissionSuppliesLiveEstimatedGas(t *testing.T) {
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	provider := backends.NewSimulatedBackend(types.GenesisAlloc{owner: {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)}}, 30000000)
	defer provider.Close()
	call := sdk.TransactionCall{To: target, Data: []byte{1, 2, 3}, Value: big.NewInt(1)}
	ctx := context.Background()
	estimated, err := provider.EstimateGas(ctx, ethereum.CallMsg{From: owner, To: &target, Data: call.Data, Value: call.Value})
	if err != nil {
		t.Fatal(err)
	}
	options, err := SubmissionOptions(ctx, provider, owner, call)
	if err != nil {
		t.Fatal(err)
	}
	if options.GasLimit < estimated+estimated/5 {
		t.Fatalf("caller budget %d cannot cover live estimate %d plus the example margin", options.GasLimit, estimated)
	}
	reverted := sdk.TransactionCall{To: target, Value: new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil)}
	if _, err = SubmissionOptions(ctx, provider, owner, reverted); err == nil {
		t.Fatal("failed independent gas estimate must prevent submission")
	}
}
