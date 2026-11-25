package main

import (
	"context"
	"encoding/json"
	"fmt"

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

func recoverAndCancel(ctx context.Context, managed *walletsession.WalletSession, saved walletsession.OperationRecord, allowReplay bool) (broadcaster.Status, error) {
	config, replay, err := scheduledRecovery(saved, allowReplay)
	if err != nil {
		return broadcaster.Status{}, err
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
