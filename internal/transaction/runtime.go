package transaction

import (
	"context"
	"errors"

	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
)

type Inventory struct {
	Notes map[common.Hash]NoteRecord

	Invalidated                     bool
	ObservedThrough, CheckedThrough *protocolrpc.CanonicalBlock
}

func EmptyInventory() Inventory {
	return Inventory{Notes: map[common.Hash]NoteRecord{}}
}

type RuntimeOptions struct {
	InvisiOptions
	FundingSigner FundingSigner
	Signer        *bind.TransactOpts
}

// ShieldPreparationReceipt is a process-local capability supplied by a managed
// session. Check verifies its wallet, connection lifetime and real capture.
type ShieldPreparationReceipt struct {
	Block protocolrpc.CanonicalBlock
	Token common.Address
	Check func(context.Context, *Runtime) error
}

type Runtime struct {
	// Only ResolveRuntime sets this after its live lookup. A restored snapshot
	// supplies neither service provenance nor a native connection epoch.
	serviceObservation *protocolrpc.DeploymentObservation
	serviceEpoch       uint64
	Snapshot           *PreparationSnapshot
	ShieldPreparation  *ShieldPreparationReceipt
	Options            RuntimeOptions
	Wallet             *Wallet
	Protocol           *chain.Protocol
	Scope              WalletScope
	RPC                *protocolrpc.ProtocolRpcClient
	ExplicitInventory  *Inventory
	ChangeAddress      string
	InventoryReader    func(context.Context) (Inventory, error)
	SummaryReader      func(context.Context) (Inventory, error)
	OnComplete         func(context.Context, *Execution) error
	OnPersist          func(context.Context, *Execution, string) error
	OnLease            func(context.Context, *Execution) error
	OnInvalidate       func(context.Context) error
	OnReprice          func(context.Context, *Execution) error
	OnExternalize      func(context.Context, *Execution) error
	SendProvider       func() protocolrpc.ChainProvider
	ObserveSendWrites  func() func() bool
	SendGeneration     func(context.Context, *Execution) (uint64, error)
}

func (i *Invisi) GetInfrastructure() InvisiOptions {
	o := i.options
	return o
}
func SnapshotWallet(ref WalletReference) (*Wallet, error) {
	if ref == nil {
		return nil, &MissingCapabilityError{Capability: "wallet"}
	}
	if w, ok := ref.(*Wallet); ok && w == nil {
		return nil, &MissingCapabilityError{Capability: "wallet"}
	}
	p := ref.publicWallet()
	if p == nil {
		return nil, &MissingCapabilityError{Capability: "public wallet"}
	}
	return newPublicWallet(p.ownerAddress, p.receivingKeys)
}
func ResolveRuntime(ctx context.Context, infrastructure InvisiOptions, wallet WalletReference, pool *chain.Protocol, namespace string) (*Runtime, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if pool == nil || infrastructure.Provider == nil || infrastructure.ProtocolRPC == nil {
		return nil, &MissingCapabilityError{Capability: "wallet, protocol, Chain RPC and Protocol RPC"}
	}
	w, e := SnapshotWallet(wallet)
	if e != nil {
		return nil, e
	}
	binding, _ := infrastructure.Provider.(*chain.HTTPBinding)
	var epoch uint64
	if binding != nil {
		epoch = binding.Epoch()
	}
	if e = pool.ValidateDeployment(ctx, infrastructure.Provider); e != nil {
		return nil, e
	}
	scope := WalletScope{ChainID: pool.Context().ChainID, ProtocolAddress: pool.Context().ProtocolAddress, OwnerAddress: w.ownerAddress, Namespace: namespace}
	if scope.Namespace == "" {
		scope.Namespace = "default"
	}
	observation, e := infrastructure.ProtocolRPC.VerifyDeployment(ctx, pool.Deployment())
	if e != nil {
		return nil, e
	}
	return &Runtime{Options: RuntimeOptions{InvisiOptions: infrastructure}, Wallet: w, Protocol: pool, Scope: scope, RPC: infrastructure.ProtocolRPC.WithProvider(infrastructure.Provider), serviceEpoch: epoch, serviceObservation: observation}, nil
}

// InitializedServiceDeployment returns the original live observation only for
// the selected runtime client, immutable deployment and native connection epoch.
func (r *Runtime) InitializedServiceDeployment(binding *chain.HTTPBinding, rpc *protocolrpc.ProtocolRpcClient, pool *chain.Protocol) *protocolrpc.DeploymentObservation {
	if r == nil || binding == nil || r.Options.Provider != binding || r.RPC != rpc || r.Protocol != pool || binding.Epoch() != r.serviceEpoch || !r.serviceObservation.Reusable() {
		return nil
	}
	return r.serviceObservation
}
func (r *Runtime) Invalidate(ctx context.Context) error {
	if r.OnInvalidate != nil {
		return r.OnInvalidate(ctx)
	}
	return ctx.Err()
}
func (e *Execution) Persist(ctx context.Context, status string) error {
	if e.Runtime.OnPersist != nil {
		return e.Runtime.OnPersist(ctx, e, status)
	}
	return ctx.Err()
}
func (e *Execution) CheckLease(ctx context.Context) error {
	if e.Runtime.OnLease != nil {
		return e.Runtime.OnLease(ctx, e)
	}
	return ctx.Err()
}
func (e *Execution) CommitBroadcasterPlan(ctx context.Context) error {
	if e.Runtime.OnReprice != nil {
		return e.Runtime.OnReprice(ctx, e)
	}
	return ctx.Err()
}

func ViewingOwner(v *ViewingCapability) common.Address { return v.identity }

// RestorePreparationRuntime checks cached deployment evidence while retaining the live send provider.
func RestorePreparationRuntime(ctx context.Context, infrastructure InvisiOptions, wallet WalletReference, pool *chain.Protocol, namespace string, snapshot *PreparationSnapshot) (*Runtime, error) {
	if pool == nil || infrastructure.Provider == nil || infrastructure.ProtocolRPC == nil {
		return nil, &MissingCapabilityError{Capability: "wallet, protocol, Chain RPC and Protocol RPC"}
	}
	w, e := SnapshotWallet(wallet)
	if e != nil {
		return nil, e
	}
	if namespace == "" {
		namespace = "default"
	}
	scope := WalletScope{ChainID: pool.Context().ChainID, ProtocolAddress: pool.Context().ProtocolAddress, OwnerAddress: w.ownerAddress, Namespace: namespace}
	if !snapshot.Match(scope, pool.Deployment()) {
		return nil, errors.New("preparation snapshot scope or deployment mismatch")
	}
	cached, e := snapshot.Clone()
	if e != nil {
		return nil, e
	}
	if e = pool.ValidateDeployment(ctx, cached.Provider(infrastructure.Provider)); e != nil {
		return nil, e
	}
	return &Runtime{Options: RuntimeOptions{InvisiOptions: infrastructure}, Wallet: w, Protocol: pool, Scope: scope, RPC: infrastructure.ProtocolRPC.WithProvider(infrastructure.Provider), Snapshot: cached}, nil
}
