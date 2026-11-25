// Resume a durable operation; a saved plan without a proof obtains fresh wallet authorization.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"

	"github.com/invisi-labs/invisi-sdk-go/examples/shared"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/invisi-labs/invisi-sdk-go/prover/native"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
	walletsqlite "github.com/invisi-labs/invisi-sdk-go/session/sqlite"
	"github.com/invisi-labs/invisi-sdk-go/wallet"
)

func main() {
	// Configure public actors and deployment.
	ctx := context.Background()
	chainRPC := flag.String("chain-rpc", "http://127.0.0.1:18565", "Chain RPC URL")
	protocolRPC := flag.String("protocol-rpc", "https://rpc.invisipay.fi/v1/chains/97", "Protocol RPC URL")
	address := flag.String("wallet-address", "", "public long receiving address")
	recipient := flag.String("recipient", "", "recipient public long receiving address")
	txHash := flag.String("transaction-hash", "", "original external transaction hash to reconcile instead of exporting")
	operationID := flag.String("operation-id", "", "durable operation to resume")
	tokenText := flag.String("token", "0x2222222222222222222222222222222222222222", "token contract")
	flag.Parse()
	if _, err := shared.RecoveryHash(*txHash, common.Hash{}); err != nil {
		log.Fatal(err)
	}

	publicWallet, err := sdk.WalletFromAddress(*address)
	if err != nil {
		log.Fatal(err)
	}

	// Authenticate the Protocol RPC deployment against Chain RPC before preparing financial intent.
	provider, err := ethclient.DialContext(ctx, *chainRPC)
	if err != nil {
		log.Fatal(err)
	}
	defer provider.Close()
	client, err := sdk.NewProtocolRpcClient(*protocolRPC)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := sdk.ProtocolFromRPC(ctx, client, provider)
	if err != nil {
		log.Fatal(err)
	}

	// Proving runs locally; this backend never executes wallet account keys.
	backend, err := native.NewNativeProver(native.NativeOptions{BBBinary: "bb", NoirBinary: "noir-execute"})
	if err != nil {
		log.Fatal(err)
	}
	invisi := sdk.NewInvisi(sdk.InvisiOptions{Provider: provider, ProtocolRPC: client, Prover: backend})
	token := common.HexToAddress(*tokenText)

	// The storage encryption key protects inventory and journals; it is independent of wallet keys.
	database, err := sql.Open("sqlite", "examples/wallet.sqlite")
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	storageKeyText := os.Getenv("INVISI_EXAMPLE_STORAGE_KEY")
	storageKey, err := wallet.Bytes(storageKeyText, 32)
	if err != nil {
		log.Fatal("application storage key: ", err)
	}
	defer clear(storageKey)
	codec, err := walletsession.NewEncryptedWalletCodec(storageKey)
	if err != nil {
		log.Fatal(err)
	}
	store, err := walletsqlite.NewWalletStore(ctx, database, codec)
	if err != nil {
		log.Fatal(err)
	}
	managed, err := walletsession.Connect(ctx, walletsession.Options{
		Invisi:    invisi,
		Wallet:    publicWallet,
		Protocol:  pool,
		Store:     store,
		Namespace: "examples",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Frozen journals contain the original selected inputs; recovery needs no discovery scan.
	snapshot, err := store.ReadSnapshot(ctx, managed.Scope())
	if err != nil {
		log.Fatal(err)
	}
	saved, ok := snapshot.Operations[*operationID]
	if !ok {
		log.Fatal("unknown operation")
	}
	hash, err := shared.RecoveryHash(*txHash, saved.TransactionHash)
	if err != nil {
		log.Fatal(err)
	}
	if saved.Status == "confirmed" {
		if err := shared.ValidateTerminalNoteRecovery(ctx, saved, managed.Scope(), provider); err != nil {
			log.Fatal(err)
		}
		log.Print("Original payment is canonically confirmed")
		return
	}
	if saved.Status == "failed" || saved.Status == "expired" || saved.Status == "reverted" {
		log.Fatalf("Original operation is terminal: %s", saved.Status)
	}
	_ = recipient
	_ = token

	// Recovery reuses the durable plan/proof, but never a persisted private wallet authorization witness.
	prepared, err := managed.Resume(ctx, *operationID)
	if err != nil {
		log.Fatal(err)
	}
	// An already exported or submitted call is reconciliation-only, including unknown hashes.
	if saved.Externalized || hash != (common.Hash{}) || saved.Status == "submitted" || saved.Status == "uncertain" {
		reconciled, err := prepared.Reconcile(ctx, walletsession.ReconcileOptions{TransactionHash: hash})
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Original operation: %s", reconciled.Status())
		return
	}
	if prepared.Status() == "prepared" || prepared.Status() == "authorized" {

		// Authorize the frozen statement with an ordinary geth EIP-712 signature.
		authorization, err := prepared.ExportWalletAuthorization(ctx)
		if err != nil {
			log.Fatal(err)
		}
		signature, err := shared.Sign(authorization, publicWallet.OwnerAddress(), "OWNER_PRIVATE_KEY")
		if err != nil {
			log.Fatal(err)
		}
		prepared, err = prepared.WithWalletSignature(ctx, signature)
		if err != nil {
			log.Fatal(err)
		}

		// Generate the local proof and independently verify it before exposing calldata.
		prepared, err = prepared.Prove(ctx)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Cached calldata can be exported offline. Before a host sends it, call prepared.ValidateSubmission(ctx).
	// A managed session send performs that live gate automatically.
	call, err := prepared.ExportTransaction(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%+v", call)
}
