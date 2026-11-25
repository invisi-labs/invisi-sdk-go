package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMalformedRecoveryHashRejectedBeforeWalletOrNetwork(t *testing.T) {
	const child = "INVISI_TEST_INVALID_RECOVERY_HASH"
	if os.Getenv(child) == "1" {
		os.Args = []string{"recovery", "--transaction-hash", "0x123", "--execute", "--resume"}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMalformedRecoveryHashRejectedBeforeWalletOrNetwork$")
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "transaction-hash must be a 32-byte hex hash") {
		t.Fatalf("malformed hash reached wallet/network: error=%v output=%s", err, output)
	}
}

func TestOriginalOperationIDCannotSilentlyCreateNewPayment(t *testing.T) {
	const child = "INVISI_TEST_ORIGINAL_ID_WITHOUT_RESUME"
	if os.Getenv(child) == "1" {
		os.Args = []string{"source-recovery", "--execute", "--operation-id", "original-payment"}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOriginalOperationIDCannotSilentlyCreateNewPayment$")
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "original operation recovery requires --resume") {
		t.Fatalf("original ID entered new intent path: error=%v output=%s", err, output)
	}
}
