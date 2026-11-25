package session

import (
	"context"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/credit"
	"github.com/invisi-labs/invisi-sdk-go/primitives"
	"github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"math/big"
	"reflect"
	"testing"
)

type creditReceiptProvider struct {
	sessionProvider
	transaction *types.Transaction
	receipt     *types.Receipt
}

func (p creditReceiptProvider) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return p.receipt, nil
}
func (p creditReceiptProvider) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return p.transaction, false, nil
}
func TestCreditReconciliationRequiresExactCanonicalTransaction(t *testing.T) {
	ctx := context.Background()
	wallet, token, _ := testSession(t)
	base := wallet.options.Provider.(sessionProvider)
	data := []byte{1, 2, 3, 4}
	to := wallet.scope.ProtocolAddress
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(31337), To: &to, Data: data})
	receipt := &types.Receipt{TxHash: tx.Hash(), BlockNumber: base.head.Number, BlockHash: base.head.Hash(), Status: types.ReceiptStatusFailed}
	wallet.options.Provider = creditReceiptProvider{base, tx, receipt}
	intent := credit.Intent{Version: 2, Context: credit.Context{ProtocolContext: wallet.coreContext(), TokenAddress: token, Timestamp: base.head.Time}, Revoke: &credit.RevokeParams{Token: token, Owner: wallet.wallet.OwnerAddress(), GrantID: big.NewInt(1), Deadline: big.NewInt(400)}}
	reconciler := wallet.CreditReconciler("latest")
	outcome, err := reconciler.Reconcile(ctx, intent, data, tx.Hash())
	if err != nil || outcome == nil || outcome.Success {
		t.Fatal("canonical reversion not authenticated", err)
	}
	if _, err = reconciler.Reconcile(ctx, intent, []byte{9}, tx.Hash()); err == nil {
		t.Fatal("accepted another transaction's calldata")
	}
	receipt.BlockHash = common.HexToHash("0xdead")
	if _, err = reconciler.Reconcile(ctx, intent, data, tx.Hash()); err == nil {
		t.Fatal("accepted noncanonical receipt")
	}
}

type creditOutputRecoveryProvider struct {
	creditReceiptProvider
	receiptBlockReads int
	reorgAt           int
	onFinal           func()
}

func (p *creditOutputRecoveryProvider) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if n != nil && n.Sign() >= 0 && n.Cmp(p.receipt.BlockNumber) == 0 {
		p.receiptBlockReads++
		if p.onFinal != nil && p.receiptBlockReads == p.reorgAt {
			p.onFinal()
		} else if p.reorgAt > 0 && p.receiptBlockReads == p.reorgAt {
			return &types.Header{Number: new(big.Int).Set(n), Extra: []byte("reorganized")}, nil
		}
	}
	return p.sessionProvider.HeaderByNumber(ctx, n)
}

func creditOutputRecoveryFixture(t *testing.T) (*WalletSession, credit.Intent, []byte, *creditOutputRecoveryProvider, StoredNote) {
	t.Helper()
	ctx := context.Background()
	s, token, _ := testSession(t)
	base := s.options.Provider.(sessionProvider)
	snapshot, err := s.options.Store.ReadSnapshot(ctx, s.scope)
	if err != nil {
		t.Fatal(err)
	}
	var note StoredNote
	for _, n := range snapshot.Notes {
		note = n
	}
	data := []byte{1, 2, 3, 4}
	to := s.scope.ProtocolAddress
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(31337), To: &to, Data: data})
	a, err := contracts.InvisiCoreMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	pack := func(name string, index uint, indexed []any, args ...any) *types.Log {
		e := a.Events[name]
		data, err := e.Inputs.NonIndexed().Pack(args...)
		if err != nil {
			t.Fatal(err)
		}
		topics := []common.Hash{e.ID}
		for _, v := range indexed {
			q, err := abi.MakeTopics([]any{v})
			if err != nil {
				t.Fatal(err)
			}
			topics = append(topics, q[0][0])
		}
		return &types.Log{Address: to, Index: index, TxHash: tx.Hash(), BlockNumber: 10, BlockHash: base.head.Hash(), Topics: topics, Data: data}
	}
	cipher, err := primitives.EncryptPrivateTransferNote(s.wallet.ViewingPublicKey(), note.Note, primitives.NoteEncryptionContext{ProtocolContext: s.coreContext(), OutputCommitment: primitives.BigIntToElement(note.Commitment.Big())})
	if err != nil {
		t.Fatal(err)
	}
	clue := make([]byte, 68)
	clue[64] = 24
	logs := []*types.Log{
		pack("CommitmentsInserted", 1, []any{big.NewInt(1)}, uint32(0), [][32]byte{note.Commitment}),
		pack("CreditDraw", 2, []any{token, big.NewInt(1), note.Commitment}, uint32(0), big.NewInt(100), big.NewInt(400), common.Address{}, big.NewInt(0), contracts.IInvisiCoreDelivery{Ciphertext: cipher, Clue: clue}),
	}
	receipt := &types.Receipt{TxHash: tx.Hash(), BlockNumber: base.head.Number, BlockHash: base.head.Hash(), Status: types.ReceiptStatusSuccessful, Logs: logs}
	provider := &creditOutputRecoveryProvider{creditReceiptProvider: creditReceiptProvider{base, tx, receipt}}
	s.options.Provider = provider
	// Keep the independent membership provider stable so the fault targets the
	// managed reconciler's final receipt fence rather than membership discovery.
	intent := credit.Intent{Version: 2, Context: credit.Context{ProtocolContext: s.coreContext(), TokenAddress: token, Timestamp: base.head.Time}, Draw: &credit.DrawParams{Token: token}}
	checked := protocolrpc.CanonicalBlock{Number: 12, Hash: common.HexToHash("0x12"), Timestamp: base.head.Time + 2}
	note.CheckedThrough = &checked
	note.Spent = true
	note.ReservedBy = "other-operation"
	if err := s.options.Store.Update(ctx, s.scope, func(state *WalletState) error { state.Notes[note.Commitment] = note; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, intent, data, provider, note
}

func TestCreditOutputReconciliationPreservesSpentObservation(t *testing.T) {
	s, intent, data, p, note := creditOutputRecoveryFixture(t)
	outcome, err := s.CreditReconciler("latest").Reconcile(context.Background(), intent, data, p.transaction.Hash())
	if err != nil || outcome == nil {
		t.Fatalf("reconcile: %v", err)
	}
	snapshot, _ := s.options.Store.ReadSnapshot(context.Background(), s.scope)
	got := snapshot.Notes[note.Commitment]
	if got.CheckedThrough == nil || *got.CheckedThrough != *note.CheckedThrough || got.Spent != note.Spent || got.ReservedBy != note.ReservedBy {
		t.Fatal("repeated authenticated output lost spent observation metadata")
	}
}

func TestCreditOutputReconciliationDoesNotWriteBeforeFinalCanonicalFence(t *testing.T) {
	s, intent, data, p, _ := creditOutputRecoveryFixture(t)
	p.reorgAt = 6
	before, _ := s.options.Store.ReadSnapshot(context.Background(), s.scope)
	_, err := s.CreditReconciler("latest").Reconcile(context.Background(), intent, data, p.transaction.Hash())
	if err == nil {
		t.Fatalf("final reorg accepted; reads=%d", p.receiptBlockReads)
	}
	after, _ := s.options.Store.ReadSnapshot(context.Background(), s.scope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("canonical failure wrote recovered Notes or journal state")
	}
}

func TestCreditOutputReconciliationRejectsInvalidatedInventoryAtCommit(t *testing.T) {
	s, intent, data, p, note := creditOutputRecoveryFixture(t)
	p.reorgAt = 6
	p.onFinal = func() {
		if err := s.options.Store.Update(context.Background(), s.scope, func(state *WalletState) error { state.Invalidated = true; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.CreditReconciler("latest").Reconcile(context.Background(), intent, data, p.transaction.Hash())
	if err == nil {
		t.Fatal("invalidated wallet accepted recovered output")
	}
	after, _ := s.options.Store.ReadSnapshot(context.Background(), s.scope)
	if !reflect.DeepEqual(after.Notes[note.Commitment], note) {
		t.Fatal("invalidated wallet received recovered output")
	}
}
