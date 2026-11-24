package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestCanonicalCodeSelectorUnsupportedOnlyForCurrentBindingEpoch(t *testing.T) {
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		reply := map[string]any{"id": req.ID, "jsonrpc": "2.0"}
		switch probes.Add(1) {
		case 1:
			reply["error"] = map[string]any{"code": -32602, "message": "unsupported block selector"}
		default:
			reply["result"] = "0x6000"
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	ctx := context.Background()
	b, err := NewHTTPBinding(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	epoch := b.Epoch()
	account := common.HexToAddress("0x42")
	hash := common.HexToHash("0x1234")
	for range 2 {
		code, supported, err := b.CodeAtCanonicalHash(ctx, account, hash)
		if err != nil || supported || code != nil || b.Epoch() != epoch {
			t.Fatalf("unsupported probe code=%x supported=%v err=%v epoch=%d", code, supported, err, b.Epoch())
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("unsupported probes=%d", probes.Load())
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = b.CodeAtCanonicalHash(cancelled, account, hash); err != context.Canceled {
		t.Fatalf("cached probe ignored cancellation: %v", err)
	}
	b.Invalidate()
	for range 2 {
		code, supported, err := b.CodeAtCanonicalHash(ctx, account, hash)
		if err != nil || !supported || len(code) == 0 {
			t.Fatalf("live probe code=%x supported=%v err=%v", code, supported, err)
		}
	}
	if probes.Load() != 3 {
		t.Fatalf("expected live code reads for supported selectors; got %d probes", probes.Load())
	}
}

func TestRetiredSelectorProbeCannotInvalidateNewEpoch(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		close(entered)
		<-resume
		_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "jsonrpc": "2.0", "error": map[string]any{"code": -32602, "message": "unsupported block selector"}})
	}))
	defer server.Close()
	ctx := context.Background()
	b, err := NewHTTPBinding(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := b.CodeAtCanonicalHash(ctx, common.HexToAddress("0x42"), common.HexToHash("0x1234"))
		done <- err
	}()
	<-entered
	b.Invalidate()
	epoch := b.Epoch()
	close(resume)
	if err = <-done; err == nil {
		t.Fatal("accepted retired probe")
	}
	if b.Epoch() != epoch {
		t.Fatal("retired capability probe invalidated the new binding epoch")
	}
}
