package shared

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
	creditsession "github.com/invisi-labs/invisi-sdk-go/session/credit"
)

// RecoveryHash reconciles only the original payment, accepting hex letter case differences.
func RecoveryHash(text string, saved common.Hash) (common.Hash, error) {
	if text == "" {
		return saved, nil
	}
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		text = text[2:]
	}
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != 32 {
		return common.Hash{}, fmt.Errorf("transaction-hash must be a 32-byte hex hash")
	}
	hash := common.BytesToHash(raw)
	if hash == (common.Hash{}) {
		return common.Hash{}, fmt.Errorf("transaction-hash must be nonzero")
	}
	if saved != (common.Hash{}) && hash != saved {
		return common.Hash{}, fmt.Errorf("transaction-hash differs from the original saved transaction")
	}
	return hash, nil
}

// ValidateTerminalCreditRecovery checks the original canonical outcome without resuming,
// proving, exporting, or applying the grant transition a second time.
func ValidateTerminalCreditRecovery(ctx context.Context, journal creditsession.Journal, reconciler creditsession.Reconciler) error {
	if (journal.Status != "confirmed" && journal.Status != "reverted") || !journal.Externalized || len(journal.Calldata) == 0 || journal.TransactionHash == (common.Hash{}) || reconciler == nil {
		return fmt.Errorf("terminal Credit journal with frozen call and transaction hash required")
	}
	outcome, err := reconciler.Reconcile(ctx, journal.Intent, append([]byte(nil), journal.Calldata...), journal.TransactionHash)
	if err != nil {
		return err
	}
	if outcome == nil {
		return creditsession.ErrConfirmationPending
	}
	if outcome.TransactionHash != journal.TransactionHash || outcome.BlockHash == (common.Hash{}) || !bytes.Equal(outcome.Calldata, journal.Calldata) || outcome.Success != (journal.Status == "confirmed") {
		return fmt.Errorf("canonical Credit receipt differs from terminal journal")
	}
	if outcome.Success && journal.Intent.Revoke != nil {
		p := journal.Intent.Revoke
		if outcome.RevokedOwner != p.Owner || outcome.RevokedToken != p.Token || outcome.RevokedGrantID == nil || outcome.RevokedGrantID.Cmp(p.GrantID) != 0 {
			return fmt.Errorf("Credit revocation event namespace mismatch")
		}
	}
	return nil
}

type RecoveryChain interface {
	TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

// ValidateTerminalNoteRecovery is for these examples' direct Core calls. It does
// not change the confirmed journal or its inventory when checking a retry.
func ValidateTerminalNoteRecovery(ctx context.Context, record walletsession.OperationRecord, scope walletsession.WalletScope, chain RecoveryChain) error {
	if record.Status != "confirmed" || !record.Externalized || record.TransactionHash == (common.Hash{}) || len(record.CallData) == 0 {
		return fmt.Errorf("confirmed Note journal with frozen call and hash required")
	}
	receipt, err := chain.TransactionReceipt(ctx, record.TransactionHash)
	if err != nil {
		return err
	}
	if receipt == nil || receipt.TxHash != record.TransactionHash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("original payment is not canonically confirmed")
	}
	header, err := chain.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return err
	}
	if header == nil || header.Number == nil || header.Number.Cmp(receipt.BlockNumber) != 0 || header.Hash() != receipt.BlockHash {
		return fmt.Errorf("original payment receipt is not canonical")
	}
	tx, pending, err := chain.TransactionByHash(ctx, record.TransactionHash)
	if err != nil {
		return err
	}
	if tx == nil || pending || tx.Hash() != record.TransactionHash || tx.To() == nil || *tx.To() != scope.ProtocolAddress || tx.ChainId().Cmp(new(big.Int).SetUint64(scope.ChainID)) != 0 || tx.Value().Sign() != 0 || !bytes.Equal(tx.Data(), record.CallData) {
		return fmt.Errorf("original payment differs from frozen call")
	}
	return nil
}
