package session

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type auditFundingPolicy struct {
	calls int
	valid bool
}

func (v *auditFundingPolicy) VerifyTypedData(context.Context, common.Address, apitypes.TypedData, []byte) (bool, error) {
	v.calls++
	return v.valid, nil
}

type auditFundingState struct {
	*submissionProvider
	wrongChain                            bool
	contract                              bool
	rejected                              bool
	payer                                 common.Address
	chainReads, codeReads, signatureCalls int
}

func (p *auditFundingState) ChainID(context.Context) (*big.Int, error) {
	p.chainReads++
	if p.wrongChain {
		return big.NewInt(97), nil
	}
	return big.NewInt(31337), nil
}
func (p *auditFundingState) CodeAt(ctx context.Context, a common.Address, b *big.Int) ([]byte, error) {
	if a == p.payer {
		p.codeReads++
		if !p.contract {
			return nil, nil
		}
	}
	return p.submissionProvider.CodeAt(ctx, a, b)
}
func (p *auditFundingState) CallContract(ctx context.Context, m ethereum.CallMsg, b *big.Int) ([]byte, error) {
	if m.To != nil && *m.To == p.payer {
		p.signatureCalls++
		out := make([]byte, 32)
		if !p.rejected {
			copy(out, []byte{0x16, 0x26, 0xba, 0x7e})
		}
		return out, nil
	}
	return p.submissionProvider.CallContract(ctx, m, b)
}
func cachedFundingValidationFixture(t *testing.T) (*Operation, *auditFundingState) {
	t.Helper()
	ctx := context.Background()
	s, token, _ := testSession(t, uint64(time.Now().Unix()))
	p := &auditFundingState{submissionProvider: &submissionProvider{sessionProvider: s.options.Provider.(sessionProvider)}, payer: s.wallet.OwnerAddress()}
	s.options.Provider = p
	s.rpc = s.rpc.WithProvider(p)
	if err := s.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	addr, _ := s.wallet.GetAddress()
	op, err := s.PrepareShield(ctx, sdk.ShieldPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: addr, Amount: big.NewInt(30)}}), Funding: sdk.FundingSource{Payer: s.wallet.OwnerAddress(), Amount: big.NewInt(30)}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := op.ExportFundingAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signFixtureRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.WithFundingSignature(ctx, sig)
	if err != nil {
		t.Fatal(err)
	}
	p.chainReads = 0
	return op, p
}
func TestCachedFundingUsesOneDeploymentIdentityCheck(t *testing.T) {
	op, p := cachedFundingValidationFixture(t)
	if err := op.ValidateSubmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.chainReads != 1 {
		t.Fatalf("chain identity reads=%d want 1", p.chainReads)
	}
}
func TestCachedFundingRetainsExplicitVerifierPolicy(t *testing.T) {
	op, p := cachedFundingValidationFixture(t)
	v := &auditFundingPolicy{}
	op.session.options.FundingVerifier = v
	err := op.ValidateSubmission(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid current funding") || v.calls != 1 {
		t.Fatalf("policy rejection lost: calls=%d err=%v", v.calls, err)
	}
	p.wrongChain = true
	v.calls = 0
	err = op.ValidateSubmission(context.Background())
	if err == nil || !strings.Contains(err.Error(), "deployment chain mismatch") || v.calls != 0 {
		t.Fatalf("wrong chain reached verifier: calls=%d err=%v", v.calls, err)
	}
}
func TestCachedFundingERC1271StateRemainsLive(t *testing.T) {
	op, p := cachedFundingValidationFixture(t)
	op.plan.Funding.Signature = []byte{1, 2, 3}
	p.contract = true
	if err := op.ValidateSubmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.rejected = true
	if err := op.ValidateSubmission(context.Background()); err == nil {
		t.Fatal("changed ERC1271 signature policy accepted")
	}
	if p.codeReads != 2 || p.signatureCalls != 2 || p.chainReads != 2 {
		t.Fatalf("live reads: code=%d signature=%d chain=%d", p.codeReads, p.signatureCalls, p.chainReads)
	}
	p.contract = false
	if err := op.ValidateSubmission(context.Background()); err == nil {
		t.Fatal("removed contract accepted")
	}
	if p.codeReads != 3 || p.signatureCalls != 2 {
		t.Fatal("code change not checked before signature call")
	}
}
func TestStandaloneFundingVerifierStillChecksNetwork(t *testing.T) {
	op, p := cachedFundingValidationFixture(t)
	typed, err := op.execution().FundingTypedData()
	if err != nil {
		t.Fatal(err)
	}
	p.wrongChain = true
	valid, err := (sdk.GethFundingVerifier{Provider: p}).VerifyTypedData(context.Background(), p.payer, typed, op.plan.Funding.Signature)
	if err == nil || valid || !strings.Contains(err.Error(), "funding verification chain mismatch") {
		t.Fatalf("standalone chain check lost: valid=%v err=%v", valid, err)
	}
	if p.chainReads != 1 {
		t.Fatal("standalone verifier did not read chain")
	}
}
func TestExplicitGethFundingVerifierKeepsOwnProvider(t *testing.T) {
	op, p := cachedFundingValidationFixture(t)
	other := &auditFundingState{submissionProvider: p.submissionProvider, wrongChain: true}
	op.session.options.FundingVerifier = sdk.GethFundingVerifier{Provider: other}
	if err := op.ValidateSubmission(context.Background()); err == nil || !strings.Contains(err.Error(), "funding verification chain mismatch") {
		t.Fatalf("explicit verifier provider bypassed: %v", err)
	}
	if p.chainReads != 1 || other.chainReads != 1 {
		t.Fatalf("provider identity checks=%d,%d", p.chainReads, other.chainReads)
	}
}
