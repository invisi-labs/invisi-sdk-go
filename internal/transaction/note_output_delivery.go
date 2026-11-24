package transaction

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/invisi-labs/invisi-sdk-go/primitives"
	"github.com/invisi-labs/invisi-sdk-go/protocol"
)

func EncryptOutput(recipient primitives.ReceivingAddress, note primitives.Note, commitment common.Hash) (protocol.Delivery, error) {
	cipher, e := primitives.EncryptPrivateTransferNote(recipient.ViewingPublicKey, note, primitives.NoteEncryptionContext{ProtocolContext: note.ProtocolContext, OutputCommitment: primitives.BigIntToElement(commitment.Big())})
	if e != nil {
		return protocol.Delivery{}, e
	}
	clue, e := primitives.CreateClue(recipient.ClueKey)
	return protocol.Delivery{Ciphertext: cipher, Clue: clue}, e
}

func (s *Runtime) Output(ctx context.Context, token common.Address, n NoteOutput) (PreparedOutput, error) {
	if n.saved == nil {
		return PreparedOutput{}, errors.New("outputs must be created with NewNoteOutput")
	}
	p := n.saved
	if e := matchPaymentRequest(p); e != nil {
		return PreparedOutput{}, e
	}
	if p.Context != s.CoreContext() || p.Token != token {
		return PreparedOutput{}, errors.New("note output chain, protocol or token mismatch")
	}
	commitment, e := primitives.ComputeNoteCommitment(p.Context.ChainID, p.Context.ProtocolAddress, primitives.BigIntToElement(p.Identity.Big()), primitives.BigIntToElement(token.Big()), primitives.BigIntToElement(p.Amount))
	if e != nil || FieldHash(commitment) != p.Commitment {
		return PreparedOutput{}, errors.New("note output commitment mismatch")
	}
	d := n.Delivery()
	if p.Pending {
		recipient, e := s.ResolveWithLookup(ctx, p.Address, p.Lookup)
		if e != nil {
			return PreparedOutput{}, e
		}
		d, e = EncryptOutput(recipient, *p.Note, p.Commitment)
		if e != nil {
			return PreparedOutput{}, e
		}
	}
	return PreparedOutput{Address: p.Address, Note: n.Note(), Amount: n.Amount(), Identity: p.Identity, Commitment: p.Commitment, Delivery: d}, nil
}

func (s *Runtime) ChangeOutput(ctx context.Context, token common.Address, address string, amount *big.Int) (PreparedOutput, error) {
	resolved := address
	if common.IsHexAddress(address) {
		recipient, err := s.ResolveRecipient(ctx, address)
		if err != nil {
			return PreparedOutput{}, err
		}
		resolved, err = primitives.EncodeLongAddress(recipient)
		if err != nil {
			return PreparedOutput{}, err
		}
	}
	n, e := NewNoteOutput(PaymentRequest{ChainID: primitives.ElementToBigInt(s.CoreContext().ChainID), ProtocolAddress: s.Scope.ProtocolAddress, TokenAddress: token, Amount: amount, Recipient: InvisiRecipient{Address: resolved}})
	if e != nil {
		return PreparedOutput{}, e
	}
	out, e := s.Output(ctx, token, n)
	if resolved != address {
		out.ChangeRecipient = resolved
		out.Address = address
	}
	return out, e
}
