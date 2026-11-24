package chain

import (
	"context"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"math/big"
	"testing"
)

type nonceOnlyProvider struct {
	protocolrpc.ChainProvider
	wrong      bool
	value      int64
	calls      int
	identities int
}

func (p *nonceOnlyProvider) ChainID(context.Context) (*big.Int, error) {
	p.identities++
	if p.wrong {
		return big.NewInt(98), nil
	}
	return big.NewInt(97), nil
}
func (p *nonceOnlyProvider) CallContract(_ context.Context, m ethereum.CallMsg, b *big.Int) ([]byte, error) {
	p.calls++
	if b != nil {
		panic("nonce helper must use latest state")
	}
	return big.NewInt(p.value).FillBytes(make([]byte, 32)), nil
}
func TestAuthorizationNonceOnlyIsCurrentAndNetworkFenced(t *testing.T) {
	registry := protocolrpc.ContractDeployment{Address: common.HexToAddress("0x11"), RuntimeCodeHash: common.HexToHash("0x1"), FromBlock: 1}
	p, _ := NewProtocol(protocolrpc.ProtocolDeployment{ChainID: "97", Protocol: registry})
	reader, ok := any(p).(interface {
		ReadAuthorizationNonce(context.Context, protocolrpc.ChainProvider, common.Address) (*big.Int, error)
	})
	if !ok {
		t.Fatal("explicit nonce-only API unavailable")
	}
	provider := &nonceOnlyProvider{value: 4}
	for _, want := range []int64{4, 5} {
		provider.value = want
		n, err := reader.ReadAuthorizationNonce(context.Background(), provider, common.HexToAddress("0x22"))
		if err != nil || n.Int64() != want {
			t.Fatal(n, err)
		}
	}
	provider.wrong = true
	if _, err := reader.ReadAuthorizationNonce(context.Background(), provider, common.HexToAddress("0x22")); err == nil {
		t.Fatal("wrong network nonce accepted")
	}
	if provider.calls != 2 || provider.identities != 3 {
		t.Fatalf("nonce calls=%d identity reads=%d", provider.calls, provider.identities)
	}
}
