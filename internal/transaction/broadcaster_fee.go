package transaction

import (
	"context"
	"errors"
	"fmt"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/invisi-labs/invisi-sdk-go/primitives"
	"github.com/invisi-labs/invisi-sdk-go/protocol"
)

func PlanFee(p Plan) (common.Address, *big.Int) {
	var r common.Address
	var f *big.Int
	switch p.Kind {
	case "shield":
		r, f = p.Shield.FeeRecipient, p.Shield.FeeAmount
	case "transact":
		r, f = p.Params.FeeRecipient, p.Params.FeeAmount

	}
	if f == nil {
		return r, new(big.Int)
	}
	return r, new(big.Int).Set(f)
}

func SetPlanFee(p *Plan, r common.Address, f *big.Int) {
	switch p.Kind {
	case "shield":
		p.Shield.FeeRecipient, p.Shield.FeeAmount = r, new(big.Int).Set(f)
	case "transact":
		p.Params.FeeRecipient, p.Params.FeeAmount = r, new(big.Int).Set(f)

	}
}

func (o *Execution) ApplyBroadcasterFee(ctx context.Context, base Plan, recipient common.Address, fee *big.Int, opt BroadcasterQuoteOptions) error {
	if opt.FeeSource == BroadcasterFeePayout {
		if base.Kind != "transact" || len(base.Inputs) == 0 || len(base.Outputs) != 0 || len(base.Params.NoteCommitments) != 0 || base.Funding.Amount == nil || base.Funding.Amount.Sign() != 0 || base.Params.WithdrawalAddress == (common.Address{}) || base.Params.WithdrawalAmount == nil || base.Params.WithdrawalAmount.Sign() <= 0 {
			return errors.New("payout fee requires a pure unfunded withdrawal")
		}
		if fee == nil || fee.Sign() <= 0 || fee.BitLen() > 128 || recipient == (common.Address{}) {
			return errors.New("invalid payout fee")
		}
		if opt.MaxFeeAmount != nil && fee.Cmp(opt.MaxFeeAmount) > 0 {
			return errors.New("broadcaster fee exceeds maximum")
		}
		_, oldFee := PlanFee(base)
		gross := new(big.Int).Add(base.Params.WithdrawalAmount, oldFee)
		sum := new(big.Int)
		for _, n := range base.Inputs {
			sum.Add(sum, primitives.ElementToBigInt(n.Note.Amount))
		}
		if sum.Cmp(gross) != 0 {
			return errors.New("payout requires the full selected note sum")
		}
		net := new(big.Int).Sub(gross, fee)
		if net.Sign() <= 0 {
			return errors.New("payout fee leaves no positive withdrawal")
		}
		p, err := ClonePlan(base)
		if err != nil {
			return err
		}
		p.Params.WithdrawalAmount = net
		p.NeedsAuthorization = true
		SetPlanFee(&p, recipient, fee)
		o.Plan = p
		return nil
	}
	_, previousFee := PlanFee(base)
	requestedFee := new(big.Int).Set(fee)
	fee = new(big.Int).Sub(fee, previousFee)
	p, e := ClonePlan(base)
	if e != nil {
		return e
	}
	SetPlanFee(&p, recipient, requestedFee)

	if fee.Sign() < 0 && p.Kind == "transact" && opt.FeeSource != BroadcasterFeePublicFunding {
		hasChange := false
		for _, out := range p.Outputs {
			if out.Change {
				hasChange = true
				break
			}
		}
		if !hasChange {
			address, e := o.Runtime.Wallet.GetAddress()
			if e != nil {
				return e
			}
			if p.ChangeAddress != "" {
				address = p.ChangeAddress
			}
			decoded, e := primitives.DecodeAddress(address)
			if e != nil || decoded.Kind != "long" {
				return fmt.Errorf("%w with resolved change recipient", broadcaster.ErrReprepareRequired)
			}
			out, e := o.Runtime.ChangeOutput(ctx, p.Context.TokenAddress, address, new(big.Int).Neg(fee))
			if e != nil {
				return e
			}
			out.Change = true
			p.Outputs = append(p.Outputs, out)
			fee = new(big.Int)
		}
	}
	change := -1
	for j, out := range p.Outputs {
		if out.Change && out.Amount.Cmp(fee) >= 0 {
			change = j
			break
		}
	}
	if opt.FeeSource == BroadcasterFeePublicFunding {
		change = -1
	}
	if change < 0 && fee.Sign() == 0 {
		o.Plan = p
		return nil
	}
	if change < 0 && opt.FeeSource == BroadcasterFeePublicFunding && p.Funding.Amount.Sign() == 0 {
		return fmt.Errorf("%w with signature funding to pay the broadcaster fee", broadcaster.ErrReprepareRequired)
	}
	if change < 0 && (opt.FeeSource == BroadcasterFeePrivateInputs || p.Funding.Amount.Sign() == 0) {
		return fmt.Errorf("%w with sufficient private inputs for the broadcaster fee", broadcaster.ErrReprepareRequired)
	}
	if change >= 0 {
		out := p.Outputs[change]
		amount := new(big.Int).Sub(out.Amount, fee)
		if amount.Sign() == 0 {
			p.Outputs = append(p.Outputs[:change], p.Outputs[change+1:]...)
		} else if fee.Sign() != 0 {
			if e = Positive(amount, 128); e != nil {
				return e
			}
			if out.Note == nil {
				return fmt.Errorf("%w with a private change opening", broadcaster.ErrReprepareRequired)
			}
			note := *out.Note
			note.Amount = primitives.BigIntToElement(amount)
			commit, e := primitives.ComputeNoteCommitment(note.ChainID, note.ProtocolAddress, primitives.BigIntToElement(out.Identity.Big()), note.TokenAddress, note.Amount)
			if e != nil {
				return e
			}
			address := out.Address
			if out.ChangeRecipient != "" {
				address = out.ChangeRecipient
			}
			recipient, e := primitives.DecodeAddress(address)
			if e != nil || recipient.Kind != "long" || recipient.OwnerAddress != note.OwnerAddress {
				return fmt.Errorf("%w with resolved change recipient keys", broadcaster.ErrReprepareRequired)
			}
			delivery, e := EncryptOutput(recipient.ReceivingAddress, note, FieldHash(commit))
			if e != nil {
				return e
			}
			out.Note = &note
			out.Amount = amount
			out.Commitment = FieldHash(commit)
			out.Delivery = delivery
			p.Outputs[change] = out
		}
		if p.Kind == "transact" {
			p.Params.NoteCommitments = nil
			for _, out := range p.Outputs {
				p.Params.NoteCommitments = append(p.Params.NoteCommitments, out.Commitment)
			}
			p.Capacity, e = protocol.SelectTransactCapacity(len(p.Inputs), len(p.Outputs))
			if e != nil {
				return e
			}
		}
		p.NeedsAuthorization = len(p.Inputs) > 0 || p.Kind != "transact"
		o.Plan = p
		return nil
	}
	gross := new(big.Int).Add(p.Funding.Amount, fee)
	if gross.Sign() <= 0 {
		return errors.New("public funding fee replacement must leave positive funding")
	}
	payer := p.Funding.Payer
	if payer == (common.Address{}) {
		return errors.New("public funding fee requires an explicit payer")
	}
	if p.FundingMode != "signature" || p.Funding.SigNonce == nil || p.Funding.Deadline == nil {
		return fmt.Errorf("%w with signature funding", broadcaster.ErrReprepareRequired)
	}
	if p.Funding.Permit.Deadline != nil && p.Funding.Permit.Deadline.Sign() > 0 {
		if opt.FundingPermit == nil {
			return fmt.Errorf("%w with a fresh token permit callback for gross funding", broadcaster.ErrReprepareRequired)
		}
		p.Funding.Permit = protocol.TokenPermit{}
	}
	p.Funding.Amount = gross
	p.Funding, e = protocol.NormalizeFunding(p.Funding)
	if e != nil {
		return e
	}
	o.Plan = p
	return nil
}
