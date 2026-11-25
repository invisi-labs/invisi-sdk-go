package main

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	walletsession "github.com/invisi-labs/invisi-sdk-go/session"
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
