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
	"github.com/invisi-labs/invisi-sdk-go/protocol"
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

type recoveryChain struct {
	tx               *types.Transaction
	receipt          *types.Receipt
	header           *types.Header
	history          map[uint64]*types.Header
	afterTransaction func()
}

func (p *recoveryChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	if p.afterTransaction != nil {
		p.afterTransaction()
	}
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
	chain.afterTransaction = func() { chain.history[12] = &types.Header{Number: big.NewInt(12), Extra: []byte("new canonical fork")} }
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

// This uses a real draw-to-Note transition and full wallet store snapshot. Terminal
// validation must never invoke the managed reconciler which imports that Note.
func TestTerminalCreditDrawRecoveryKeepsEntireInventoryReadOnlyAcrossReorg(t *testing.T) {
	ctx := context.Background()
	state := credit.State{AvailableCredit: primitives.ElementFromUint64(400), ProtocolContext: primitives.ProtocolContext{ChainID: primitives.ElementFromUint64(1), ProtocolAddress: primitives.ElementFromUint64(17)}, TokenAddress: primitives.ElementFromUint64(34), SpenderWalletIdentity: primitives.ElementFromUint64(33), OwnerWalletIdentity: primitives.ElementFromUint64(44), GrantID: primitives.ElementFromUint64(1), Nonce: primitives.ElementFromUint64(1), Expiry: primitives.ElementFromUint64(2100000000), CreditSharedKey: primitives.ElementFromUint64(2)}
	c := credit.Context{ProtocolContext: state.ProtocolContext, TokenAddress: common.HexToAddress("0x22"), Timestamp: 2000000000}
	transition, err := state.Draw(primitives.ElementFromUint64(7), c.Timestamp)
	if err != nil {
		t.Fatal(err)
	}
	params := credit.DrawParams{Token: c.TokenAddress, Sources: []protocol.InputSource{{TreeId: big.NewInt(1), MerkleRoot: common.HexToHash("0x1")}}, Payers: []credit.Payer{{Payer: common.HexToAddress("0x2c"), Amount: big.NewInt(7)}}, GrantRefs: []credit.GrantRef{{PayerIndex: big.NewInt(0), GrantID: big.NewInt(1)}}, Nullifiers: []common.Hash{common.Hash(transition.Nullifier.Bytes())}, CreditCommitments: []common.Hash{common.Hash(transition.StateCommitment.Bytes())}, RecoveryAmounts: []common.Hash{common.Hash(transition.RecoveryAmount.Bytes())}, TotalAmount: big.NewInt(7), Deadline: big.NewInt(2000000300)}
	receiver, err := primitives.DeriveWallet(common.LeftPadBytes([]byte{99}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := &primitives.ReceivingKeys{ViewingPublicKey: receiver.ViewingPublicKey, ClueKey: receiver.ClueKey}
	note := primitives.Note{ProtocolContext: state.ProtocolContext, OwnerAddress: receiver.OwnerAddress, TokenAddress: state.TokenAddress, Amount: primitives.ElementFromUint64(7), Nonce: primitives.ElementFromUint64(100), Payload: []byte("draw invoice")}
	clue := make([]byte, 68)
	clue[64] = 24
	deliveries := []protocol.Delivery{{Ciphertext: []byte{0x84}, Clue: clue}, {Ciphertext: []byte{0x84}, Clue: clue}}
	prepared, err := credit.PrepareDrawToNote(c, params, deliveries, []credit.LocatedState{{State: state, TreeID: 1, Debit: primitives.ElementFromUint64(7)}}, note, keys)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := prepared.ExportIntent()
	if err != nil {
		t.Fatal(err)
	}
	data, err := intent.Draw.Calldata(c, make([]byte, 32), intent.Deliveries)
	if err != nil {
		t.Fatal(err)
	}
	to := common.HexToAddress("0x11")
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), To: &to, Data: data})
	journal := creditsession.Journal{ID: "original-draw", Status: "confirmed", Intent: intent, Calldata: data, TransactionHash: tx.Hash(), Externalized: true}
	scope := walletsession.WalletScope{ChainID: 1, ProtocolAddress: to}
	store := walletsession.NewMemoryWalletStore()
	commitment := intent.Draw.NoteCommitment
	if err := store.Update(ctx, scope, func(s *walletsession.WalletState) error {
		s.PostpaidCredit.Operations[journal.ID] = journal
		s.Notes[commitment] = walletsession.StoredNote{Note: note, Commitment: commitment, Spent: true, ReservedBy: "another-intent"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	header := &types.Header{Number: big.NewInt(10)}
	chain := &recoveryChain{tx: tx, header: header, receipt: &types.Receipt{TxHash: tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: header.Hash(), Status: types.ReceiptStatusSuccessful}}
	if err := ValidateTerminalCreditRecovery(ctx, journal, scope, chain); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ReadSnapshot(ctx, scope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal draw imported Notes or changed wallet inventory")
	}
	chain.afterTransaction = func() { chain.header = &types.Header{Number: big.NewInt(10), Extra: []byte("reorganized")} }
	if err := ValidateTerminalCreditRecovery(ctx, journal, scope, chain); err == nil {
		t.Fatal("receipt changed during final canonical check")
	}
	after, _ = store.ReadSnapshot(ctx, scope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed final canonical check wrote inventory")
	}
}
