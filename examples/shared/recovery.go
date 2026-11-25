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
	"github.com/invisi-labs/invisi-sdk-go/primitives"
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

// ValidateTerminalCreditRecovery validates frozen terminal Credit evidence through
// chain reads only. Managed Credit reconciliation can import output Notes.
func ValidateTerminalCreditRecovery(ctx context.Context, journal creditsession.Journal, scope walletsession.WalletScope, chain RecoveryChain) error {
	if (journal.Status != "confirmed" && journal.Status != "reverted") || !journal.Externalized || len(journal.Calldata) == 0 || journal.TransactionHash == (common.Hash{}) {
		return fmt.Errorf("terminal Credit journal with frozen call and transaction hash required")
	}
	if primitives.ElementToBigInt(journal.Intent.Context.ChainID).Cmp(new(big.Int).SetUint64(scope.ChainID)) != 0 || common.BigToAddress(primitives.ElementToBigInt(journal.Intent.Context.ProtocolAddress)) != scope.ProtocolAddress {
		return fmt.Errorf("saved Credit intent deployment mismatch")
	}
	return validateTerminalCall(ctx, journal.Calldata, journal.TransactionHash, journal.Status == "confirmed", scope, chain)
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
	// ResolvedAt is the canonical inventory observation, which can be later than
	// the payment receipt. Re-inclusion of the payment alone cannot repair it.
	if record.ResolvedAt == nil || record.ResolvedAt.Hash == (common.Hash{}) {
		return fmt.Errorf("confirmed Note journal resolution checkpoint required")
	}
	if err := validateTerminalCall(ctx, record.CallData, record.TransactionHash, true, scope, chain); err != nil {
		return err
	}
	// Check the inventory observation last: a reorg at this later height can leave
	// the earlier receipt canonical while invalidating the saved spent observation.
	resolvedNumber := new(big.Int).SetUint64(record.ResolvedAt.Number)
	resolvedHeader, err := chain.HeaderByNumber(ctx, resolvedNumber)
	if err != nil {
		return err
	}
	if resolvedHeader == nil || resolvedHeader.Number == nil || resolvedHeader.Number.Cmp(resolvedNumber) != 0 || resolvedHeader.Hash() != record.ResolvedAt.Hash {
		return fmt.Errorf("saved Note resolution checkpoint is not canonical; refresh wallet recovery state")
	}
	return nil
}

// Only chain reads are permitted here. Managed Credit reconciliation also imports
// Note outputs and therefore must not be used to validate an already terminal call.
func validateTerminalCall(ctx context.Context, data []byte, hash common.Hash, success bool, scope walletsession.WalletScope, chain RecoveryChain) error {
	receipt, err := chain.TransactionReceipt(ctx, hash)
	if err != nil {
		return err
	}
	wantStatus := uint64(types.ReceiptStatusFailed)
	if success {
		wantStatus = types.ReceiptStatusSuccessful
	}
	if receipt == nil || receipt.TxHash != hash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.Status != wantStatus {
		return fmt.Errorf("original payment receipt differs from saved terminal status")
	}
	// Keep both reads anchored to the original receipt even if a provider reuses objects.
	blockNumber := new(big.Int).Set(receipt.BlockNumber)
	blockHash := receipt.BlockHash
	header, err := chain.HeaderByNumber(ctx, blockNumber)
	if err != nil {
		return err
	}
	if header == nil || header.Number == nil || header.Number.Cmp(blockNumber) != 0 || header.Hash() != blockHash {
		return fmt.Errorf("original payment receipt is not canonical")
	}
	tx, pending, err := chain.TransactionByHash(ctx, hash)
	if err != nil {
		return err
	}
	if tx == nil || pending || tx.Hash() != hash || tx.To() == nil || *tx.To() != scope.ProtocolAddress || tx.ChainId().Cmp(new(big.Int).SetUint64(scope.ChainID)) != 0 || tx.Value().Sign() != 0 || !bytes.Equal(tx.Data(), data) {
		return fmt.Errorf("original payment differs from frozen call")
	}
	final, err := chain.HeaderByNumber(ctx, blockNumber)
	if err != nil {
		return err
	}
	if final == nil || final.Number == nil || final.Number.Cmp(blockNumber) != 0 || final.Hash() != blockHash {
		return fmt.Errorf("original payment receipt changed during validation")
	}
	return nil
}

// ValidateRecoverySnapshot prevents receipt-only recovery from accepting inventory
// that the managed session has already invalidated after a canonical fork change.
func ValidateRecoverySnapshot(snapshot walletsession.WalletState) error {
	if snapshot.Invalidated {
		return fmt.Errorf("wallet requires reset refresh before recovery")
	}
	return nil
}
