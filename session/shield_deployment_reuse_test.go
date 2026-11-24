package session

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type shieldDeploymentService struct {
	requests atomic.Int64
	fail     atomic.Bool
}

func newDeploymentSession(t *testing.T, mode string) (*WalletSession, common.Address, *chain.HTTPBinding, *shieldAnchorWire, *shieldDeploymentService) {
	t.Helper()
	base, token, binding, wire := anchorWireSession(t)
	service := new(shieldDeploymentService)
	deployment := base.protocol.Deployment()
	if mode == "predeployment" {
		deployment.Protocol.FromBlock = 11
		pool, err := chain.NewProtocol(deployment)
		if err != nil {
			t.Fatal(err)
		}
		base.protocol = pool
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID     uint64
			Method string
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Method != "invisi_getDeployment" {
			t.Errorf("unexpected deployment request %+v: %v", q, err)
			return
		}
		service.requests.Add(1)
		if service.fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": deployment}})
	}))
	t.Cleanup(server.Close)
	options := protocolrpc.ClientOptions{Deployment: &deployment, UseNativeHTTPTransport: mode == "native" || mode == "new epoch" || mode == "predeployment"}
	if mode == "custom service" {
		options.HTTPClient = server.Client()
	}
	rpc, err := sdk.NewProtocolRpcClient(server.URL+"/v1", options)
	if err != nil {
		t.Fatal(err)
	}
	infra := base.options.InvisiOptions
	infra.ProtocolRPC = rpc
	s, err := Connect(context.Background(), Options{Invisi: sdk.NewInvisi(infra), Wallet: base.wallet, Protocol: base.protocol, Store: NewMemoryWalletStore()})
	if err != nil {
		t.Fatal(err)
	}
	if service.requests.Load() != 1 {
		t.Fatal("Connect must authenticate the selected service")
	}
	wire.reads = nil
	return s, token, binding, wire, service
}

func TestShieldPrewarmReusesOnlyAuthenticatedDeploymentFacts(t *testing.T) {
	for _, mode := range []string{"native", "new epoch", "custom service", "default service"} {
		t.Run(mode, func(t *testing.T) {
			s, token, b, wire, service := newDeploymentSession(t, mode)
			if mode == "new epoch" {
				b.Invalidate()
			}
			if err := s.InitializeShieldPreparation(context.Background(), token); err != nil {
				t.Fatal(err)
			}
			wantService := int64(1)
			wantReads := []string{"eth_getBlockByNumber/\"latest\"", "eth_chainId", "eth_getCode/" + token.Hex()}
			if mode == "new epoch" || mode == "custom service" {
				wantService++
			}
			if mode == "new epoch" {
				wantReads = append(wantReads, "eth_getCode/"+s.scope.ProtocolAddress.Hex(), "eth_call")
			}
			got := slices.Clone(wire.reads)
			slices.Sort(got)
			slices.Sort(wantReads)
			if service.requests.Load() != wantService || !slices.Equal(got, wantReads) {
				t.Fatalf("total service requests=%d want %d, prewarm wire reads=%v want %v", service.requests.Load(), wantService, got, wantReads)
			}
		})
	}
}

func TestDeploymentRefreshFailureDuringPrewarmRetiresServiceProof(t *testing.T) {
	s, token, _, wire, service := newDeploymentSession(t, "native")
	ctx := context.Background()
	wire.duringToken = func() {
		service.fail.Store(true)
		if _, err := s.options.ProtocolRPC.GetDeployment(ctx, s.scope.ChainID); err == nil {
			t.Error("public GetDeployment skipped fresh failure")
		}
	}
	if err := s.InitializeShieldPreparation(ctx, token); err == nil {
		t.Fatal("promoted service proof retired during token capture")
	}
	if err := s.InitializeShieldPreparation(ctx, token); err == nil {
		t.Fatal("reused failed service proof on retry")
	}
}

func TestRestoredPrewarmRequiresLiveServiceEvenWithNativeTransportAndPins(t *testing.T) {
	ctx := context.Background()
	base, token, binding, _ := anchorWireSession(t)
	if err := base.RefreshState(ctx); err != nil {
		t.Fatal(err)
	}
	deployment := base.protocol.Deployment()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := protocolrpc.NewProtocolRpcClient(server.URL+"/v1", protocolrpc.ClientOptions{UseNativeHTTPTransport: true, Deployment: &deployment})
	if err != nil {
		t.Fatal(err)
	}
	infra := base.options.InvisiOptions
	infra.ProtocolRPC, infra.Provider = client, binding
	s, err := Connect(ctx, Options{Invisi: sdk.NewInvisi(infra), Wallet: base.wallet, Protocol: base.protocol, Namespace: base.scope.Namespace, Store: base.options.Store})
	if err != nil || requests.Load() != 0 {
		t.Fatalf("offline Connect: requests=%d err=%v", requests.Load(), err)
	}
	if err = s.InitializeShieldPreparation(ctx, token); err == nil || requests.Load() != 1 {
		t.Fatalf("restored pins bypassed service authentication: requests=%d err=%v", requests.Load(), err)
	}
}

type shieldReadHookStore struct {
	WalletStore
	beforeRead func()
}

func (s *shieldReadHookStore) ReadSnapshot(ctx context.Context, scope WalletScope) (WalletState, error) {
	if hook := s.beforeRead; hook != nil {
		s.beforeRead = nil
		hook()
	}
	return s.WalletStore.ReadSnapshot(ctx, scope)
}

func TestSupersededShieldInitializerCannotRetireWinningServiceObservation(t *testing.T) {
	ctx := context.Background()
	s, token, _, _, service := newDeploymentSession(t, "custom service")
	next := common.HexToAddress("0x3333")
	s.options.Store = &shieldReadHookStore{WalletStore: s.options.Store, beforeRead: func() {
		if err := s.InitializeShieldPreparation(ctx, next); err != nil {
			t.Fatal(err)
		}
	}}
	if err := s.InitializeShieldPreparation(ctx, token); !errors.Is(err, errShieldCaptureSuperseded) {
		t.Fatalf("older initialize: %v", err)
	}
	op, err := s.PrepareShield(ctx, anchorInput(s, next, s.wallet.OwnerAddress()))
	if err != nil || !op.plan.RetainedShieldAnchor || service.requests.Load() != 2 {
		t.Fatalf("older initializer retired winner: prepare=%v total service calls=%d", err, service.requests.Load())
	}
}

type shieldCommitHookStore struct {
	WalletStore
	afterCommit func()
}

func (s *shieldCommitHookStore) Update(ctx context.Context, scope WalletScope, f func(*WalletState) error) error {
	err := s.WalletStore.Update(ctx, scope, f)
	if err == nil && s.afterCommit != nil {
		hook := s.afterCommit
		s.afterCommit = nil
		hook()
	}
	return err
}

func TestSupersededShieldRefreshPreservesCommittedSyncAndNewerCapture(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := anchorWireSession(t)
	next := common.HexToAddress("0x3333")
	s.options.Store = &shieldCommitHookStore{WalletStore: s.options.Store, afterCommit: func() {
		if err := s.InitializeShieldPreparation(ctx, next); err != nil {
			t.Fatal(err)
		}
	}}
	if err := s.RefreshState(ctx); err != nil {
		t.Fatalf("sync committed but returned error: %v", err)
	}
	state, err := s.options.Store.ReadSnapshot(ctx, s.scope)
	if err != nil || state.PreparationSnapshot == nil {
		t.Fatalf("missing sync snapshot: %v", err)
	}
	op, err := s.PrepareShield(ctx, anchorInput(s, next, s.wallet.OwnerAddress()))
	if err != nil || !op.plan.RetainedShieldAnchor {
		t.Fatalf("newer capture lost: %v", err)
	}
}

func TestShieldCaptureRejectsPredeploymentHeadWithExplicitNonce(t *testing.T) {
	ctx := context.Background()
	s, token, _, _, _ := newDeploymentSession(t, "predeployment")
	initErr := s.InitializeShieldPreparation(ctx, token)
	input := anchorInput(s, token, s.wallet.OwnerAddress())
	input.Funding.SigNonce = big.NewInt(23)
	op, err := s.PrepareShield(ctx, input)
	if initErr == nil || (err == nil && op.plan.RetainedShieldAnchor) {
		t.Fatalf("predeployment initialization=%v, explicit nonce prepare=%v retained=%v", initErr, err, op != nil && op.plan.RetainedShieldAnchor)
	}
}
