package broadcaster

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type tokenSupportInitializer interface {
	InitializeTokenSupport(context.Context, common.Address, Trust, func(context.Context) error) (bool, error)
}

func TestInitializedPreparationCapabilityReuseAndRevocation(t *testing.T) {
	data, trust, _ := intent(t)
	request, _ := DecodeIntent(data)
	recipient := common.HexToAddress("0x33")
	var statuses, probes atomic.Int64
	var invalid, changed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": trust.ChainID.String(), "protocol": trust.Protocol, "sender": trust.Signer, "feeRecipient": recipient, "tokenAddress": request.Token, "supported": true}
		switch req.Method {
		case "broadcaster_getTokenSupport":
			probes.Add(1)
		case "broadcaster_getStatus":
			statuses.Add(1)
		default:
			t.Errorf("unexpected %s", req.Method)
		}
		if invalid.Load() {
			result["feeLock"] = nil
		}
		if changed.Load() {
			result["feeLock"] = FeeLockCapabilities{Version: "1", Model: feeLockModel, Core: true}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	client, _ := NewClient(Configuration{server.URL, trust.Signer, recipient}, Options{server.Client()})
	init, ok := any(client).(tokenSupportInitializer)
	if !ok {
		t.Fatal("explicit token support preparation initialization unavailable")
	}
	var revoked atomic.Bool
	guard := func(ctx context.Context) error {
		if revoked.Load() {
			return errors.New("retired receipt")
		}
		return ctx.Err()
	}
	initialize := func() {
		t.Helper()
		supported, err := init.InitializeTokenSupport(context.Background(), request.Token, trust, guard)
		if err != nil || !supported {
			t.Fatal(supported, err)
		}
	}
	prepare := func() {
		t.Helper()
		if got, err := client.ForPreparation(context.Background(), data, trust); err != nil || got != client {
			t.Fatal(got, err)
		}
	}
	initialize()
	prepare()
	prepare()
	if probes.Load() != 1 || statuses.Load() != 0 {
		t.Fatalf("probes=%d statuses=%d", probes.Load(), statuses.Load())
	}
	if _, err := client.ServiceStatus(context.Background(), trust); err != nil {
		t.Fatal(err)
	}
	prepare()
	if statuses.Load() != 1 {
		t.Fatal("unchanged live status retired preparation capability")
	}
	wrong := trust
	wrong.ChainID = new(big.Int).Add(trust.ChainID, big.NewInt(1))
	if _, err := client.ForPreparation(context.Background(), data, wrong); err == nil {
		t.Fatal("wrong trust reused capability")
	}
	prepare()
	changed.Store(true)
	if supported, err := client.SupportsToken(context.Background(), request.Token, trust); err != nil || !supported {
		t.Fatal(supported, err)
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("fresh token support observation retained stale downgrade authority")
	}
	changed.Store(false)
	initialize()
	changed.Store(true)
	if _, err := client.ServiceStatus(context.Background(), trust); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("changed live capability retained legacy downgrade authority")
	}
	changed.Store(false)
	initialize()
	invalid.Store(true)
	if _, err := client.ServiceStatus(context.Background(), trust); err == nil {
		t.Fatal("null fee lock accepted")
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("failed live status retained authority")
	}
	invalid.Store(false)
	initialize()
	revoked.Store(true)
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("retired receipt accepted")
	}
	revoked.Store(false)
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("retired capability restored")
	}
	initialize()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := init.InitializeTokenSupport(canceled, request.Token, trust, guard); err == nil {
		t.Fatal("canceled refresh accepted")
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("canceled refresh retained authority")
	}
}

func TestPreparationRefreshAndLiveResponsesCannotReplaceNewerAuthority(t *testing.T) {
	data, trust, _ := intent(t)
	request, _ := DecodeIntent(data)
	recipient := common.HexToAddress("0x33")
	type heldCall struct {
		release chan struct{}
		method  string
	}
	entered := make(chan heldCall, 1)
	var hold atomic.Bool
	var failed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fail := failed.Load()
		if hold.Swap(false) {
			call := heldCall{make(chan struct{}), req.Method}
			entered <- call
			<-call.release
		}
		if fail {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "unavailable"}})
			return
		}
		result := map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": trust.ChainID.String(), "protocol": trust.Protocol, "sender": trust.Signer, "feeRecipient": recipient, "tokenAddress": request.Token, "supported": true}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	client, _ := NewClient(Configuration{server.URL, trust.Signer, recipient}, Options{server.Client()})
	init, ok := any(client).(tokenSupportInitializer)
	if !ok {
		t.Fatal("initializer missing")
	}
	initialize := func() error {
		_, err := init.InitializeTokenSupport(context.Background(), request.Token, trust, func(ctx context.Context) error { return ctx.Err() })
		return err
	}
	if err := initialize(); err != nil {
		t.Fatal(err)
	}
	hold.Store(true)
	oldDone := make(chan error, 1)
	go func() { oldDone <- initialize() }()
	call := <-entered
	if call.method != "broadcaster_getTokenSupport" {
		t.Fatal(call.method)
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err == nil {
		t.Fatal("refresh admission retained old authority")
	}
	if err := initialize(); err != nil {
		t.Fatal(err)
	}
	close(call.release)
	if err := <-oldDone; err == nil {
		t.Fatal("superseded refresh succeeded")
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err != nil {
		t.Fatal("superseded refresh erased new authority", err)
	}
	failed.Store(true)
	hold.Store(true)
	go func() { _, err := client.ServiceStatus(context.Background(), trust); oldDone <- err }()
	call = <-entered
	failed.Store(false)
	if err := initialize(); err != nil {
		t.Fatal(err)
	}
	close(call.release)
	if err := <-oldDone; err == nil {
		t.Fatal("held status failure missing")
	}
	if _, err := client.ForPreparation(context.Background(), data, trust); err != nil {
		t.Fatal("older failure revoked new initialization", err)
	}
}
