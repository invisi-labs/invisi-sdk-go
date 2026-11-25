// Resume a Credit journal without reconstructing its grant transition or spending reserved inputs twice.
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
	"github.com/invisi-labs/invisi-sdk-go/prover"
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
	operationID := flag.String("operation-id", "", "durable operation to resume")
	txHash := flag.String("transaction-hash", "", "external transaction hash to reconcile instead of exporting")
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

	// Recovery uses the encrypted frozen journal, without a fresh inventory scan.
	creditSession, err := managed.Credit()
	if err != nil {
		log.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(ctx, managed.Scope())
	if err != nil {
		log.Fatal(err)
	}
	if err := shared.ValidateRecoverySnapshot(snapshot); err != nil {
		log.Fatal(err)
	}
	saved, ok := snapshot.PostpaidCredit.Operations[*operationID]
	if !ok {
		log.Fatal("unknown Credit operation")
	}
	hash, err := shared.RecoveryHash(*txHash, saved.TransactionHash)
	if err != nil {
		log.Fatal(err)
	}
	status := saved.Status
	if status == "confirmed" || status == "reverted" {
		if err := shared.ValidateTerminalCreditRecovery(ctx, saved, managed.CreditReconciler("latest")); err != nil {
			log.Fatal(err)
		}
		log.Printf("Original Credit operation is canonically %s", status)
		return
	}
	if status == "failed" {
		log.Fatal("Original Credit operation is terminal: failed")
	}
	operation, err := creditSession.Resume(ctx, *operationID, backend)
	if err != nil {
		log.Fatal(err)
	}
	// Never sign or export a payment which already left this process.
	if saved.Externalized || hash != (common.Hash{}) || status == "submitted" || status == "uncertain" {
		if hash == (common.Hash{}) {
			log.Fatal("operation already externalized; supply original transaction-hash for reconciliation")
		}
		if saved.TransactionHash == (common.Hash{}) {
			if err := operation.Externalize(ctx, hash); err != nil {
				log.Fatal(err)
			}
		}
		if err := operation.Reconcile(ctx, managed.CreditReconciler("latest")); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Authorization witnesses are intentionally absent from storage, so unproved transitions need host approval again.
	if status == "prepared" || status == "authorized" {
		preview, err := operation.Preview(ctx)
		if err != nil {
			log.Fatal(err)
		}
		// Direct-owner revocation has no wallet-spending authorization or proof.
		if preview.Kind != "revoke-credit" {
			authorization, err := operation.ExportSigningRequest(ctx)
			if err != nil {
				log.Fatal(err)
			}
			signature, err := shared.Sign(authorization, publicWallet.OwnerAddress(), "OWNER_PRIVATE_KEY")
			if err != nil {
				log.Fatal(err)
			}
			if err = operation.WithWalletSignature(ctx, signature); err != nil {
				log.Fatal(err)
			}
			if err = operation.Prove(ctx, backend, prover.Options{}); err != nil {
				log.Fatal(err)
			}
		}
	}

	// The frozen transition retains its reservation when calldata leaves this process.
	call, err := operation.Calldata(ctx, publicWallet.OwnerAddress())
	if err != nil {
		log.Fatal(err)
	}
	if err = operation.Externalize(ctx, common.Hash{}); err != nil {
		log.Fatal(err)
	}
	log.Printf("%+v", call)
}
