package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSavedCancellationNeverCreatesSubmissionUnlessRecoveringUnknownAcknowledgement(t *testing.T) {
	config := broadcaster.Configuration{Endpoint: "https://relay.example", QuoteSigner: common.HexToAddress("0x11"), FeeRecipient: common.HexToAddress("0x22")}
	for _, test := range []struct {
		name, id                              string
		request, allow, wantReplay, wantError bool
	}{
		{"known ID", "saved-id", true, false, false, false},
		{"lost ack requires explicit frozen replay", "", true, false, false, true},
		{"lost ack permitted", "", true, true, true, false},
		{"never submitted", "", false, true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request *broadcaster.Submission
			if test.request {
				request = &broadcaster.Submission{}
			}
			plan := map[string]any{"Broadcaster": map[string]any{"Configuration": config, "RequestID": test.id, "Request": request, "Signed": broadcaster.SignedQuote{Quote: broadcaster.Quote{Schedule: &broadcaster.Schedule{PublishAfter: "2000000000"}}}}}
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			got, replay, err := scheduledRecovery(walletsession.OperationRecord{Plan: raw}, test.allow)
			if (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && (got != config || replay != test.wantReplay) {
				t.Fatalf("saved capability changed: %v replay=%v", got, replay)
			}
		})
	}
}

func TestRecoveryFlagsCannotFallThroughToNewScheduledPayment(t *testing.T) {
	const child = "INVISI_TEST_SCHEDULED_RECOVERY_FLAGS"
	if mode := os.Getenv(child); mode != "" {
		os.Args = []string{"scheduled-cancel"}
		if mode == "missing-id" {
			os.Args = append(os.Args, "--recover-submit-id")
		} else {
			os.Args = append(os.Args, "--operation-id", "")
		}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}
	for _, mode := range []string{"missing-id", "empty-id"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryFlagsCannotFallThroughToNewScheduledPayment$")
			cmd.Env = append(os.Environ(), child+"="+mode)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "recovery requires a nonempty --operation-id") {
				t.Fatalf("recovery flags entered creation/network path: error=%v output=%s", err, output)
			}
		})
	}
}

func TestSavedCancellationSetupSkipsSourceBootstrapAndProverAssets(t *testing.T) {
	t.Setenv("PROVENANCE_ENDPOINT", "https://unavailable-source.invalid")
	for _, name := range []string{"PROVENANCE_SIGNING_PUBLIC_KEY", "PROVENANCE_SIGNING_KEY_ID", "PROVENANCE_GENERATION", "PROVENANCE_PROVIDER_ID"} {
		t.Setenv(name, "")
	}
	t.Setenv("BB_BINARY", "/unavailable/bb")
	t.Setenv("NOIR_EXECUTOR", "/unavailable/noir")
	t.Setenv("PROVENANCE_ASSET_ROOT", t.TempDir())
	capabilities, err := scheduledCapabilities("original-operation", nil)
	if err != nil {
		t.Fatalf("saved cancellation opened Source/prover setup: %v", err)
	}
	if capabilities.Prover != nil || capabilities.Provenance != nil {
		t.Fatal("saved cancellation must use only the durable capability")
	}
}

type cancelledRecoveryChain struct{ header *types.Header }

func (c *cancelledRecoveryChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return c.header, nil
}
func TestCompletedCancellationRechecksResolutionWithoutSessionOrRelay(t *testing.T) {
	header := &types.Header{Number: big.NewInt(12)}
	raw := []byte(`{"Broadcaster":{"Configuration":{},"Signed":{"Quote":{"Schedule":{"publishAfter":"2000000000"}}},"RequestID":"original-server-id","cancellationConfirmed":true}}`)
	saved := walletsession.OperationRecord{ID: "original", Status: "cancelled", Plan: raw, Externalized: true, ResolvedAt: &protocolrpc.CanonicalBlock{Number: 12, Hash: header.Hash()}}
	chain := &cancelledRecoveryChain{header: header}
	before, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoverAndCancel(context.Background(), nil, saved, false, chain); err != nil {
		t.Fatal(err)
	}
	chain.header = &types.Header{Number: big.NewInt(12), Extra: []byte("orphaned observation")}
	if _, err := recoverAndCancel(context.Background(), nil, saved, false, chain); err == nil {
		t.Fatal("orphaned completed cancellation accepted")
	}
	after, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("terminal cancellation validation changed journal")
	}
}
