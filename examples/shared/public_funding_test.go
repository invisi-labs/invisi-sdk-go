package shared

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/protocol"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"math/big"
	"testing"
)

func TestPublicFundingOutputsAreExecutableAndPreservePayment(t *testing.T) {
	request := sdk.PaymentRequest{ChainID: big.NewInt(97), ProtocolAddress: common.HexToAddress("0x1111111111111111111111111111111111111111"), TokenAddress: common.HexToAddress("0x2222222222222222222222222222222222222222"), Amount: big.NewInt(10), Recipient: sdk.InvisiRecipient{Address: "0i0e0525SFRf53yh2aljmm3sn9JQ5nJk7LqWt0G2ajnRY2pe698y550QXhp9uELXpcCZMf5Y608rRDJ68jJgfEWy5j7U5e5eVNPVz8vW2W2kmyw4p9lVLwW3z3WeFHQs5KdfR980CX"}}
	outputs, err := PublicFundingOutputs(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = protocol.SelectTransactCapacity(0, len(outputs)); err != nil {
		t.Fatalf("public-funded example outputs cannot execute: %v", err)
	}
	total := new(big.Int)
	for _, output := range outputs {
		total.Add(total, output.Amount())
		if output.Note() == nil || output.Note().OwnerAddress != outputs[0].Note().OwnerAddress {
			t.Fatal("payment recipient changed")
		}
	}
	if total.Cmp(big.NewInt(10)) != 0 || request.Amount.Cmp(big.NewInt(10)) != 0 {
		t.Fatal("gross amount changed")
	}
	if len(outputs) != 2 || outputs[0].Commitment() == outputs[1].Commitment() {
		t.Fatal("two independently encrypted outputs required")
	}
}

func TestPublicFundingOutputsRejectsUnsplittableAmount(t *testing.T) {
	if _, err := PublicFundingOutputs(sdk.PaymentRequest{Amount: big.NewInt(1), Recipient: sdk.InvisiRecipient{Address: "unused"}}); err == nil {
		t.Fatal("one base unit cannot form two positive outputs")
	}
}
