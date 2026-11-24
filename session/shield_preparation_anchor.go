package session

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	transaction "github.com/invisi-labs/invisi-sdk-go/internal/transaction"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
)

// A successful native capture records only historical construction facts. The
// receipt is process-local and scoped to the session's actual connections.
type shieldPreparationAnchor struct {
	service                   *protocolrpc.DeploymentObservation
	binding                   *chain.HTTPBinding
	epoch                     uint64
	protocol                  *chain.Protocol
	rpc                       *protocolrpc.ProtocolRpcClient
	scope                     WalletScope
	block                     protocolrpc.CanonicalBlock
	tokens                    map[common.Address]common.Hash
	generation, resetRevision uint64
	observed, checked         *protocolrpc.CanonicalBlock
}

// WalletSession copies share this state. Admission orders replacement of the
// wallet ticket and reservation of a service request generation.
type shieldPreparationState struct {
	atomic.Pointer[shieldPreparationAnchor]
	admission sync.Mutex
}

var errShieldCaptureSuperseded = errors.New("Shield initialization superseded")

func (s *WalletSession) beginShieldCapture() *shieldPreparationAnchor {
	if s.shieldPreparations == nil {
		return nil
	}
	binding, _ := s.options.Provider.(*chain.HTTPBinding)
	ticket := &shieldPreparationAnchor{binding: binding, protocol: s.protocol, rpc: s.rpc, scope: s.scope}
	if binding != nil {
		ticket.epoch = binding.Epoch()
	}
	s.shieldPreparations.admission.Lock()
	s.shieldPreparations.Store(ticket)
	s.shieldPreparations.admission.Unlock()
	return ticket
}

func (a *shieldPreparationAnchor) current(ctx context.Context, s *WalletSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	serviceCurrent := a.service == nil || a.service.Current()
	if s.shieldPreparations.Load() != a {
		return errShieldCaptureSuperseded
	}
	if !serviceCurrent {
		return errors.New("Protocol RPC deployment refresh superseded Shield capture")
	}
	if a.binding == nil || a.binding != s.options.Provider || a.binding.Epoch() != a.epoch || a.protocol != s.protocol || a.rpc != s.rpc || a.scope != s.scope {
		return errors.New("Shield preparation capture changed")
	}
	return nil
}

func (a *shieldPreparationAnchor) checkState(v *WalletState) error {
	if v.Invalidated || v.Scan.Generation != a.generation || v.Scan.ResetRevision != a.resetRevision || !reflect.DeepEqual(v.ObservedThrough, a.observed) || !reflect.DeepEqual(v.CheckedThrough, a.checked) {
		return errors.New("wallet changed since Shield preparation capture")
	}
	return nil
}

func (a *shieldPreparationAnchor) checkWalletState(ctx context.Context, s *WalletSession) error {
	v, err := s.metadataSnapshot(ctx)
	if err != nil {
		return err
	}
	if err = a.checkState(&v); err != nil {
		return err
	}
	if s.publicHistory != nil {
		public, err := s.publicHistory.Snapshot(ctx)
		if err != nil {
			return err
		}
		if public.Generation != a.generation {
			return errors.New("public history changed since Shield preparation capture")
		}
	}
	return nil
}
func (a *shieldPreparationAnchor) checkWallet(ctx context.Context, s *WalletSession) error {
	if err := a.checkWalletState(ctx, s); err != nil {
		return err
	}
	return a.current(ctx, s)
}

func blockFromHeader(h *types.Header) (protocolrpc.CanonicalBlock, error) {
	if h == nil || h.Number == nil || !h.Number.IsUint64() {
		return protocolrpc.CanonicalBlock{}, errors.New("invalid preparation block")
	}
	return protocolrpc.CanonicalBlock{Number: h.Number.Uint64(), Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}, nil
}

// InitializeShieldPreparation captures a historical block and verifies token code
// for subsequent signature-funded Shields on this native HTTP session. A new
// initialization replaces the previous receipt even if its reads fail. A nil
// funding nonce is still read from the current network for every operation.
func (s *WalletSession) InitializeShieldPreparation(ctx context.Context, token common.Address) error {
	ticket := s.beginShieldCapture()
	if ticket == nil || ticket.binding == nil {
		return errors.New("Shield initialization requires an owned HTTP binding")
	}
	if _, err := s.protocol.ForToken(token); err != nil {
		return err
	}
	state, err := s.metadataSnapshot(ctx)
	if err != nil {
		return err
	}
	if state.Invalidated {
		return errors.New("wallet requires reset scan")
	}
	service, err := s.shieldCaptureService(ctx, ticket, true)
	if err != nil {
		return err
	}
	head, err := ticket.binding.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	block, err := blockFromHeader(head)
	if err != nil {
		return err
	}
	// Core code, VERSION and the optional immutable registry binding belong to
	// the native connection epoch. Only the token and checkpoints below are
	// historical observations at this new capture.
	if err = s.protocol.ValidateDeployment(ctx, ticket.binding); err != nil {
		return err
	}
	if block.Number < s.protocol.Deployment().Protocol.FromBlock {
		return errors.New("Shield preparation block predates Core deployment")
	}
	snap := newPreparationSnapshot(s, state, block, head)
	recorder := transaction.NewSnapshotRecorder(ticket.binding, snap, s.protocol)
	if err = verifyShieldCheckpoints(ctx, recorder, state.ObservedThrough, state.CheckedThrough); err != nil {
		return err
	}
	code, supported, err := ticket.binding.CodeAtCanonicalHash(ctx, token, block.Hash)
	if err != nil {
		return err
	}
	if !supported {
		code, err = recorder.CodeAt(ctx, token, new(big.Int).SetUint64(block.Number))
		if err != nil {
			return err
		}
		if err = s.canonical(ctx, block); err != nil {
			return err
		}
	}
	if len(code) == 0 {
		return errors.New("token address must have contract code")
	}
	return s.publishShieldCapture(ctx, ticket, service, state, map[common.Address]common.Hash{token: crypto.Keccak256Hash(code)}, block)
}

// Headers in this recorder come only from the active native capture. Check
// original private markers before a refresh can replace them with its new head.
func verifyShieldCheckpoints(ctx context.Context, r *transaction.SnapshotRecorder, points ...*protocolrpc.CanonicalBlock) error {
	for _, point := range points {
		if point == nil {
			continue
		}
		if point.Number > r.Snapshot.Block.Number {
			return errors.New("Shield checkpoint ahead of preparation block")
		}
		h := r.Snapshot.Headers[point.Number]
		if h == nil {
			var err error
			h, err = r.HeaderByNumber(ctx, new(big.Int).SetUint64(point.Number))
			if err != nil {
				return err
			}
		}
		block, err := blockFromHeader(h)
		if err != nil || block != *point {
			return errors.New("Shield preparation checkpoint changed")
		}
	}
	return nil
}

// Claim any fresh service lookup before another wallet capture can replace the
// ticket. A superseded caller must never advance the service generation.
func (s *WalletSession) shieldCaptureService(ctx context.Context, ticket *shieldPreparationAnchor, allowInitialized bool) (*protocolrpc.DeploymentObservation, error) {
	state := s.shieldPreparations
	state.admission.Lock()
	if err := ticket.current(ctx, s); err != nil {
		state.admission.Unlock()
		return nil, err
	}
	if allowInitialized {
		if service := s.runtime.InitializedServiceDeployment(ticket.binding, ticket.rpc, ticket.protocol); service != nil {
			state.admission.Unlock()
			return service, nil
		}
	}
	verify := ticket.rpc.BeginDeploymentVerification(ctx, ticket.protocol.Deployment())
	state.admission.Unlock()
	return verify()
}

func (s *WalletSession) publishShieldCapture(ctx context.Context, ticket *shieldPreparationAnchor, service *protocolrpc.DeploymentObservation, state WalletState, tokens map[common.Address]common.Hash, block protocolrpc.CanonicalBlock) error {
	serviceCurrent := service.Current()
	if err := ticket.current(ctx, s); err != nil {
		return err
	}
	if !serviceCurrent {
		return errors.New("Protocol RPC deployment refresh superseded Shield capture")
	}
	next := *ticket
	next.service = service
	next.block, next.tokens = block, tokens
	next.generation, next.resetRevision = state.Scan.Generation, state.Scan.ResetRevision
	next.observed, next.checked = state.ObservedThrough, state.CheckedThrough
	// Check both stores before publication. A refresh that started later owns
	// the new ticket and cannot be overwritten by an old response.
	stateErr := next.checkWalletState(ctx, s)
	if err := ticket.current(ctx, s); err != nil {
		return err
	}
	if stateErr != nil {
		return stateErr
	}
	if !s.shieldPreparations.CompareAndSwap(ticket, &next) {
		return errShieldCaptureSuperseded
	}
	if err := next.current(ctx, s); err != nil {
		s.shieldPreparations.CompareAndSwap(&next, ticket)
		return err
	}
	return nil
}
