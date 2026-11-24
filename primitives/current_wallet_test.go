package primitives

import (
	"encoding/hex"
	"testing"
)

func TestEVMAccountKeyRestoresCurrentWallet(t *testing.T) {
	key := make([]byte, 32)
	key[31] = 1
	wallet, err := DeriveWallet(key)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(wallet.ViewingPrivateKey) != "57f1898fae64848970967e21804b8c1fc08559fea986b2f80644e1da35cbcc7b" {
		t.Fatal("viewing KDF differs from the shared receiving signature vector")
	}
}

func TestRegistrationIsNotPrivateWalletAuthorization(t *testing.T) {
	for _, operation := range []WalletOperation{"register-address", "update-address"} {
		request := WalletAuthorizationRequest{Context: ProtocolContext{ChainID: ElementFromUint64(1), ProtocolAddress: ElementFromUint64(2)}, Operation: operation}
		if _, err := WalletAuthorizationTypedData(request); err == nil {
			t.Fatal("registry authorization remained in private wallet operations")
		}
	}
}
