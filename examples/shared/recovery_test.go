package shared

import (
	"context"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/credit"
	"github.com/invisi-labs/invisi-sdk-go/primitives"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
	creditsession "github.com/invisi-labs/invisi-sdk-go/session/credit"
)

func TestRecoveryHashRejectsMalformedAndConflictingHashes(t *testing.T) {
	saved := common.HexToHash("0x" + strings.Repeat("ab", 32))
	for _, text := range []string{"0x1", "garbage", "0x0X" + strings.Repeat("ab", 32), "0x" + strings.Repeat("00", 32), "0x" + strings.Repeat("cd", 32)} {
		if _, err := RecoveryHash(text, saved); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	for _, text := range []string{"", "0x" + strings.Repeat("AB", 32)} {
		got, err := RecoveryHash(text, saved)
		if err != nil || got != saved {
			t.Fatalf("matching saved hash: %v %s", err, got)
		}
	}
}

type recoveryReconciler func(context.Context, credit.Intent, []byte, common.Hash) (*creditsession.Outcome, error)

func (f recoveryReconciler) Reconcile(ctx context.Context, i credit.Intent, d []byte, h common.Hash) (*creditsession.Outcome, error) {
	return f(ctx, i, d, h)
}
func TestTerminalCreditRecoveryValidatesWithoutJournalMutation(t *testing.T) {
	ctx := context.Background()
	managed, err := creditsession.New(creditsession.Options{Store: creditsession.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	c := credit.Context{ProtocolContext: primitives.ProtocolContext{ChainID: primitives.ElementFromUint64(1), ProtocolAddress: primitives.ElementFromUint64(17)}, TokenAddress: common.HexToAddress("0x22"), Timestamp: 2000000000}
	p := credit.RevokeParams{Token: c.TokenAddress, Owner: common.HexToAddress("0x44"), GrantID: big.NewInt(1), Deadline: big.NewInt(2000000300)}
	prepared, err := credit.PrepareRevoke(c, p)
	if err != nil {
		t.Fatal(err)
	}
	op, err := managed.Prepare(ctx, "revoke", prepared)
	if err != nil {
		t.Fatal(err)
	}
	data, err := op.Calldata(ctx, p.Owner)
	if err != nil {
		t.Fatal(err)
	}
	hash := common.HexToHash("0x123")
	intent, err := prepared.ExportIntent()
	if err != nil {
		t.Fatal(err)
	}
	journal := creditsession.Journal{ID: "revoke", Status: "confirmed", Intent: intent, Calldata: data, TransactionHash: hash, Externalized: true}
	before := journal
	outcome := &creditsession.Outcome{TransactionHash: hash, Calldata: data, BlockHash: common.HexToHash("0x456"), Success: true, RevokedOwner: p.Owner, RevokedToken: p.Token, RevokedGrantID: p.GrantID}
	calls := 0
	reconciler := recoveryReconciler(func(_ context.Context, i credit.Intent, d []byte, h common.Hash) (*creditsession.Outcome, error) {
		calls++
		if i.Revoke == nil || !reflect.DeepEqual(d, data) || h != hash {
			t.Fatal("frozen identity changed")
		}
		return outcome, nil
	})
	if err = ValidateTerminalCreditRecovery(ctx, journal, reconciler); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(){func() { outcome.Success = false }, func() { outcome.Success = true; outcome.Calldata = []byte{1} }, func() { outcome.Calldata = data; outcome.TransactionHash = common.HexToHash("0x999") }, func() { outcome = nil }} {
		change()
		if err = ValidateTerminalCreditRecovery(ctx, journal, reconciler); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
	if calls != 5 || !reflect.DeepEqual(before, journal) {
		t.Fatal("validation did not remain read only")
	}
}

type recoveryChain struct {
	tx      *types.Transaction
	receipt *types.Receipt
	header  *types.Header
	history map[uint64]*types.Header
}

func (p *recoveryChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return p.tx, false, nil
}
func (p *recoveryChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return p.receipt, nil
}
func (p *recoveryChain) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if p.history != nil {
		return p.history[number.Uint64()], nil
	}
	return p.header, nil
}
func TestTerminalNoteRecoveryValidatesCanonicalFrozenCallReadOnly(t *testing.T) {
	to := common.HexToAddress("0x11")
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), To: &to, Data: []byte{1, 2}, Value: big.NewInt(0)})
	header := &types.Header{Number: big.NewInt(10)}
	chain := &recoveryChain{tx: tx, header: header, receipt: &types.Receipt{TxHash: tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: header.Hash(), Status: types.ReceiptStatusSuccessful}}
	scope := walletsession.WalletScope{ChainID: 1, ProtocolAddress: to}
	record := walletsession.OperationRecord{ID: "original", Status: "confirmed", CallData: []byte{1, 2}, TransactionHash: tx.Hash(), Externalized: true, ResolvedAt: &protocolrpc.CanonicalBlock{Number: 10, Hash: header.Hash()}}
	if err := ValidateTerminalNoteRecovery(context.Background(), record, scope, chain); err != nil {
		t.Fatal(err)
	}
	chain.receipt.BlockHash = common.HexToHash("0x999")
	if err := ValidateTerminalNoteRecovery(context.Background(), record, scope, chain); err == nil {
		t.Fatal("reorg accepted")
	}
	chain.receipt.BlockHash = header.Hash()
	record.CallData = []byte{3}
	if err := ValidateTerminalNoteRecovery(context.Background(), record, scope, chain); err == nil {
		t.Fatal("different frozen call accepted")
	}
}

func TestTerminalNoteRecoveryRejectsReincludedPaymentOnChangedResolutionFork(t *testing.T) {
	to := common.HexToAddress("0x11")
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), To: &to, Data: []byte{1, 2}, Value: big.NewInt(0)})
	receiptHeader := &types.Header{Number: big.NewInt(10)}
	resolution := &types.Header{Number: big.NewInt(12), Extra: []byte("original observation fork")}
	record := walletsession.OperationRecord{ID: "original", Status: "confirmed", CallData: []byte{1, 2}, TransactionHash: tx.Hash(), Externalized: true, ResolvedAt: &protocolrpc.CanonicalBlock{Number: 12, Hash: resolution.Hash()}}
	chain := &recoveryChain{tx: tx, receipt: &types.Receipt{TxHash: tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: receiptHeader.Hash(), Status: types.ReceiptStatusSuccessful}, history: map[uint64]*types.Header{10: receiptHeader, 12: resolution}}
	scope := walletsession.WalletScope{ChainID: 1, ProtocolAddress: to}
	if err := ValidateTerminalNoteRecovery(context.Background(), record, scope, chain); err != nil {
		t.Fatal("receipt height may predate the resolution observation", err)
	}
	before := record
	chain.history[12] = &types.Header{Number: big.NewInt(12), Extra: []byte("new canonical fork")}
	if err := ValidateTerminalNoteRecovery(context.Background(), record, scope, chain); err == nil {
		t.Fatal("accepted stale inventory resolution despite canonical re-included payment")
	}
	if !reflect.DeepEqual(before, record) {
		t.Fatal("read-only validation changed resolved journal")
	}
}

func TestRecoveryRejectsInvalidatedInventory(t *testing.T) {
	if err := ValidateRecoverySnapshot(walletsession.WalletState{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecoverySnapshot(walletsession.WalletState{Invalidated: true}); err == nil {
		t.Fatal("invalidated inventory accepted for read-only terminal recovery")
	}
}
