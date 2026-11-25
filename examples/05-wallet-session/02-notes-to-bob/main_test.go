package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A recovery ID must never silently create a new payment intent.
func TestNewTransferRejectsResumeOperationID(t *testing.T) {
	const child = "INVISI_EXAMPLE_TEST_RESUME_FLAG"
	if os.Getenv(child) == "1" {
		os.Args = []string{"notes-to-bob", "--operation-id", "retained-payment"}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNewTransferRejectsResumeOperationID$")
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "flag provided but not defined: -operation-id") {
		t.Fatalf("new transfer must reject a recovery ID before wallet/network work; error=%v output=%s", err, output)
	}
}
