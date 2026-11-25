package shared

import (
	"fmt"
	"math/big"

	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

// PublicFundingOutputs pays the same complete receiving address in two Notes.
// A public-funded transact requires at least two outputs; its gross amount stays
// unchanged. Nonces are generated independently for the two encrypted deliveries.
func PublicFundingOutputs(request sdk.PaymentRequest) ([]sdk.NoteOutput, error) {
	recipient, ok := request.Recipient.(sdk.InvisiRecipient)
	if !ok || recipient.Nonce != nil || request.Amount == nil || request.Amount.Cmp(big.NewInt(2)) < 0 {
		return nil, fmt.Errorf("public-funded example requires at least two base units and a full recipient without a pinned nonce")
	}
	first := new(big.Int).Div(new(big.Int).Set(request.Amount), big.NewInt(2))
	amounts := []*big.Int{first, new(big.Int).Sub(request.Amount, first)}
	outputs := make([]sdk.NoteOutput, 0, 2)
	for _, amount := range amounts {
		part := request
		part.Amount = amount
		output, err := sdk.NewNoteOutput(part)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}
