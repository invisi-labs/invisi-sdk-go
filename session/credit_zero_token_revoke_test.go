package session

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/credit"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type tokenlessRevokeProvider struct {
	sessionProvider
	invalidDeployment, invalidHeader bool
}

func (p tokenlessRevokeProvider) CodeAt(ctx context.Context, address common.Address, block *big.Int) ([]byte, error) {
	if p.invalidDeployment {
		return []byte{0xff}, nil
	}
	if address == (common.Address{}) || address == common.HexToAddress("0x1234") {
		return nil, nil
	}
	return p.sessionProvider.CodeAt(ctx, address, block)
}
func (p tokenlessRevokeProvider) HeaderByNumber(ctx context.Context, block *big.Int) (*types.Header, error) {
	if p.invalidHeader {
		return &types.Header{}, nil
	}
	return p.sessionProvider.HeaderByNumber(ctx, block)
}

func TestCreditZeroFeeRevocationDoesNotRequireTokenContract(t *testing.T) {
	for _, mode := range []struct {
		name    string
		managed bool
	}{
		{name: "explicit"},
		{name: "managed", managed: true},
	} {
		for _, token := range []common.Address{{}, common.HexToAddress("0x1234")} {
			t.Run(token.Hex()+"/"+mode.name, func(t *testing.T) {
				s, _, _ := testSession(t)
				p := tokenlessRevokeProvider{sessionProvider: s.options.Provider.(sessionProvider)}
				s.options.Provider = p
				s.rpc = s.rpc.WithProvider(p)
				req := credit.RevokePreparation{TokenAddress: token, GrantID: big.NewInt(2)}
				if mode.managed {
					c, e := s.Credit()
					if e != nil {
						t.Fatal(e)
					}
					op, e := c.PrepareRevoke(context.Background(), "tokenless", req)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = op.Calldata(context.Background(), s.wallet.OwnerAddress()); e != nil {
						t.Fatal(e)
					}
				} else {
					opts := s.options.InvisiOptions
					opts.Provider = p
					opts.ProtocolRPC = s.rpc
					i := sdk.NewInvisi(opts)
					op, e := i.PrepareRevokeCredit(context.Background(), sdk.ExplicitRevokeCreditPreparation{
						TransactionIdentity: sdk.TransactionIdentity{Wallet: s.wallet, Protocol: s.protocol},
						RevokePreparation:   req,
					})
					if e != nil {
						t.Fatal(e)
					}
					intent, e := op.ExportIntent()
					if e != nil || intent.Revoke.Token != token || intent.Revoke.FeeAmount.Sign() != 0 {
						t.Fatalf("wrong revocation namespace or fee: %v", e)
					}
					if _, e = op.Calldata(s.wallet.OwnerAddress()); e != nil {
						t.Fatal(e)
					}
				}
			})
		}
	}
}
func TestTokenlessRevocationRetainsPreparationValidation(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		invalidDeployment, invalidHeader bool
		token                            common.Address
		deadline                         uint64
		fee                              *credit.Fee
	}{
		{name: "deployment", invalidDeployment: true},
		{name: "header", invalidHeader: true},
		{name: "expired deadline", deadline: 1},
		{name: "positive fee zero token", fee: &credit.Fee{Recipient: common.HexToAddress("0x5678"), Amount: big.NewInt(1)}},
		{name: "positive fee EOA token", token: common.HexToAddress("0x1234"), fee: &credit.Fee{Recipient: common.HexToAddress("0x5678"), Amount: big.NewInt(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := testSession(t)
			p := tokenlessRevokeProvider{sessionProvider: s.options.Provider.(sessionProvider), invalidDeployment: tc.invalidDeployment, invalidHeader: tc.invalidHeader}
			opts := s.options.InvisiOptions
			opts.Provider = p
			opts.ProtocolRPC = s.rpc.WithProvider(p)
			i := sdk.NewInvisi(opts)
			_, e := i.PrepareRevokeCredit(context.Background(), sdk.ExplicitRevokeCreditPreparation{
				TransactionIdentity: sdk.TransactionIdentity{Wallet: s.wallet, Protocol: s.protocol},
				RevokePreparation:   credit.RevokePreparation{TokenAddress: tc.token, GrantID: big.NewInt(2), Deadline: tc.deadline, Fee: tc.fee},
			})
			if e == nil {
				t.Fatal("invalid revocation accepted")
			}
		})
	}
}
