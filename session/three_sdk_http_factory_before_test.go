//go:build three_sdk_before

package session

import (
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
	"testing"
)

func threeSDKProvider(t *testing.T, endpoint string) sdk.Provider {
	t.Helper()
	p, err := ethclient.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
