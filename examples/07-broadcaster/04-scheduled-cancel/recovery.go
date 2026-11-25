package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/examples/shared"
	"github.com/invisi-labs/invisi-sdk-go/prover/native"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"math/big"
	"os"

	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
)

// Only a frozen admission attempt with an unknown opaque server ID can be replayed.
func scheduledRecovery(saved walletsession.OperationRecord, allowReplay bool) (broadcaster.Configuration, bool, error) {
	var plan struct {
		Broadcaster *struct {
			Configuration broadcaster.Configuration
			Signed        struct {
				Quote struct{ Schedule *broadcaster.Schedule }
			}
			Request   json.RawMessage
			RequestID string
		}
	}
	if err := json.Unmarshal(saved.Plan, &plan); err != nil {
		return broadcaster.Configuration{}, false, err
	}
	if plan.Broadcaster == nil || plan.Broadcaster.Signed.Quote.Schedule == nil {
		return broadcaster.Configuration{}, false, fmt.Errorf("original operation has no saved scheduled Broadcaster capability")
	}
	relay := plan.Broadcaster
	if relay.RequestID != "" {
		return relay.Configuration, false, nil
	}
	if len(relay.Request) == 0 || string(relay.Request) == "null" {
		return broadcaster.Configuration{}, false, fmt.Errorf("original operation was never submitted; cancellation must not create a payment")
	}
	if !allowReplay {
		return broadcaster.Configuration{}, false, fmt.Errorf("submission ID unknown; --recover-submit-id explicitly replays the original frozen request before cancellation")
	}
	return relay.Configuration, true, nil
}

func recoverAndCancel(ctx context.Context, managed *walletsession.WalletSession, saved walletsession.OperationRecord, allowReplay bool, chain recoveryHeaders) (broadcaster.Status, error) {
	config, replay, err := scheduledRecovery(saved, allowReplay)
	if err != nil {
		return broadcaster.Status{}, err
	}
	if saved.Status == "cancelled" {
		return validateCompletedCancellation(ctx, saved, chain)
	}

	recovered, err := managed.Resume(ctx, saved.ID)
	if err != nil {
		return broadcaster.Status{}, err
	}
	relay, err := broadcaster.NewClient(config)
	if err != nil {
		return broadcaster.Status{}, err
	}
	if replay {
		if _, err := relay.Submit(ctx, recovered); err != nil {
			return broadcaster.Status{}, err
		}
	}
	// The persisted cancellation capability is sufficient, including retries after
	// a lost acknowledgement or failed canonical cleanup. No wallet key is used.
	return relay.Cancel(ctx, recovered)
}

// Saved cancellation authenticates its retained relay capability; it needs no
// Source bootstrap or local proving runtime, even for acknowledged offline retry.
func scheduledCapabilities(operationID string, pool *sdk.Protocol) (sdk.InvisiOptions, error) {
	if operationID != "" {
		return sdk.InvisiOptions{}, nil
	}
	backend, err := native.NewNativeProver(native.NativeOptions{BBBinary: os.Getenv("BB_BINARY"), NoirBinary: os.Getenv("NOIR_EXECUTOR")})
	if err != nil {
		return sdk.InvisiOptions{}, err
	}
	source, err := shared.SourceCapability(pool)
	if err != nil {
		return sdk.InvisiOptions{}, err
	}
	return sdk.InvisiOptions{Prover: backend, Provenance: source}, nil
}

type recoveryHeaders interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

func validateCompletedCancellation(ctx context.Context, saved walletsession.OperationRecord, chain recoveryHeaders) (broadcaster.Status, error) {
	var plan struct {
		Broadcaster *struct {
			RequestID             string
			CancellationConfirmed bool
		}
	}
	if err := json.Unmarshal(saved.Plan, &plan); err != nil {
		return broadcaster.Status{}, err
	}
	if saved.Status != "cancelled" || !saved.Externalized || saved.TransactionHash != (common.Hash{}) || saved.ResolvedAt == nil || saved.ResolvedAt.Hash == (common.Hash{}) || plan.Broadcaster == nil || !plan.Broadcaster.CancellationConfirmed || plan.Broadcaster.RequestID == "" {
		return broadcaster.Status{}, fmt.Errorf("completed cancellation requires its acknowledged canonical resolution")
	}
	number := new(big.Int).SetUint64(saved.ResolvedAt.Number)
	header, err := chain.HeaderByNumber(ctx, number)
	if err != nil {
		return broadcaster.Status{}, err
	}
	if header == nil || header.Number == nil || header.Number.Cmp(number) != 0 || header.Hash() != saved.ResolvedAt.Hash {
		return broadcaster.Status{}, fmt.Errorf("saved cancellation resolution is not canonical; refresh wallet recovery state")
	}
	return broadcaster.Status{ID: plan.Broadcaster.RequestID, State: "cancelled"}, nil
}
