package main

import (
	"encoding/json"
	"flag"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
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
