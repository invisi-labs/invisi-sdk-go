//go:build !three_sdk_before

package session

import (
	"context"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"testing"
)

func threeSDKProvider(t *testing.T, endpoint string) sdk.Provider {
	t.Helper()
	p, err := chain.NewHTTPBinding(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
