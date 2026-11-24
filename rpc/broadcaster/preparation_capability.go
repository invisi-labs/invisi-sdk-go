package broadcaster

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// preparationAuthority belongs to one client; scoped fee-lock clients share its
// revocation state, but cannot initialize or impersonate the original owner.
type preparationAuthority struct {
	mu         sync.Mutex
	owner      *Client
	generation uint64
	bound      bool
	current    *preparationCapability
	ticket     *preparationFeeLock
	fetch      uint64
}
type preparationCapability struct {
	localPolicy      bool
	status           ServiceStatus
	chain            string
	protocol, signer common.Address
	configuration    Configuration
	valid            func(context.Context) error
}

// InitializeTokenSupport explicitly captures the service capability carried by
// getTokenSupport (the server embeds its getStatus result). valid binds reuse to
// the caller's discovery receipt lifetime. Plain SupportsToken stays a live probe.
// A refresh retires previous authority before any IO, including failed refreshes.
func (c *Client) InitializeTokenSupport(ctx context.Context, token common.Address, trust Trust, valid func(context.Context) error) (bool, error) {
	return c.initializeTokenSupport(ctx, token, trust, valid, false)
}

// InitializeTokenSupportWithLocalPolicy binds a nonblocking lifetime check to
// discovery. The SDK can repeat that check after the final network or store read.
func (c *Client) InitializeTokenSupportWithLocalPolicy(ctx context.Context, token common.Address, trust Trust, valid func(context.Context) error) (bool, error) {
	return c.initializeTokenSupport(ctx, token, trust, valid, true)
}
func (c *Client) initializeTokenSupport(ctx context.Context, token common.Address, trust Trust, valid func(context.Context) error, localPolicy bool) (bool, error) {
	a := c.preparation
	if a == nil || a.owner != c {
		return false, errors.New("preparation initialization requires original client")
	}
	a.mu.Lock()
	a.generation++
	generation := a.generation
	a.bound = true
	a.current = nil
	a.ticket = nil
	a.fetch++
	a.mu.Unlock()
	if valid == nil {
		return false, errors.New("preparation receipt validity required")
	}
	supported, status, err := c.tokenSupport(ctx, token, trust)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if !supported {
		return false, nil
	}
	if trust.Signer != (common.Address{}) && trust.Signer != c.configuration.QuoteSigner {
		return false, errors.New("invalid preparation signer")
	}
	record := &preparationCapability{localPolicy: localPolicy, status: copyPreparationStatus(status), chain: trust.ChainID.String(), protocol: trust.Protocol, signer: c.configuration.QuoteSigner, configuration: c.configuration, valid: valid}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.generation != generation {
		return false, errors.New("preparation initialization superseded")
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	a.current = record
	return true, nil
}
func copyPreparationStatus(s ServiceStatus) ServiceStatus {
	if s.FeeLock != nil {
		v := *s.FeeLock
		s.FeeLock = &v
	}
	return s
}
func (c *Client) preparationSnapshot() (*preparationCapability, bool) {
	if c.preparation == nil {
		return nil, false
	}
	c.preparation.mu.Lock()
	defer c.preparation.mu.Unlock()
	return c.preparation.current, c.preparation.bound
}
func (c *Client) retirePreparation(record *preparationCapability) {
	if record == nil || c.preparation == nil {
		return
	}
	c.preparation.mu.Lock()
	defer c.preparation.mu.Unlock()
	if c.preparation.current == record {
		c.preparation.current = nil
		c.preparation.ticket = nil
		c.preparation.fetch++
		c.preparation.generation++
	}
}
func (c *Client) checkPreparation(ctx context.Context, record *preparationCapability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record == nil {
		return errors.New("preparation capability retired; initialize broadcasters again")
	}
	if record.configuration != c.configuration {
		return errors.New("preparation client configuration changed")
	}
	if err := record.valid(ctx); err != nil {
		c.retirePreparation(record)
		return err
	}
	current, _ := c.preparationSnapshot()
	if current != record || (c.preparationReceipt != nil && c.preparationReceipt != record) {
		return errors.New("preparation capability retired")
	}
	return nil
}
func (c *Client) preparationStatus(ctx context.Context, trust Trust) (ServiceStatus, *preparationCapability, error) {
	record, bound := c.preparationSnapshot()
	if !bound {
		status, err := c.readServiceStatus(ctx, trust)
		return status, nil, err
	}
	if c.preparation.owner != c {
		return ServiceStatus{}, nil, errors.New("preparation requires original client")
	}
	if record == nil {
		return ServiceStatus{}, nil, c.checkPreparation(ctx, record)
	}
	if trust.ChainID == nil || trust.ChainID.String() != record.chain || trust.Protocol != record.protocol || (trust.Signer != (common.Address{}) && trust.Signer != record.signer) {
		return ServiceStatus{}, nil, errors.New("preparation trust mismatch")
	}
	if err := c.checkPreparation(ctx, record); err != nil {
		return ServiceStatus{}, nil, err
	}
	return copyPreparationStatus(record.status), record, nil
}
func (c *Client) observePreparation(record *preparationCapability, status ServiceStatus, err error) {
	if record == nil {
		return
	}
	if err != nil || status.SubmissionHalted || !sameFeeLockCapability(record.status.FeeLock, status.FeeLock) {
		c.retirePreparation(record)
	}
}
func sameFeeLockCapability(a, b *FeeLockCapabilities) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func decodeServiceStatus(raw json.RawMessage) (ServiceStatus, error) {
	var status ServiceStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return status, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return status, err
	}
	if fields == nil {
		return status, errors.New("invalid broadcaster service status")
	}
	if value, present := fields["feeLock"]; present && string(value) == "null" {
		return status, errors.New("invalid fee lock capability")
	}
	return status, nil
}
func (c *Client) readServiceStatus(ctx context.Context, trust Trust) (ServiceStatus, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "broadcaster_getStatus", nil, &raw); err != nil {
		return ServiceStatus{}, err
	}
	status, err := decodeServiceStatus(raw)
	if err != nil {
		return status, err
	}
	if err = ctx.Err(); err != nil {
		return status, err
	}
	return c.checkedServiceStatus(status, trust)
}
func (c *Client) tokenSupport(ctx context.Context, token common.Address, trust Trust) (bool, ServiceStatus, error) {
	if token == (common.Address{}) || trust.ChainID == nil {
		return false, ServiceStatus{}, errors.New("token and deployment context required")
	}
	var raw json.RawMessage
	if err := c.call(ctx, "broadcaster_getTokenSupport", map[string]any{"tokenAddress": token.Hex()}, &raw); err != nil {
		return false, ServiceStatus{}, err
	}
	status, err := decodeServiceStatus(raw)
	if err != nil {
		return false, status, err
	}
	status, err = c.checkedServiceStatus(status, trust)
	if err != nil {
		return false, status, err
	}
	var support struct {
		TokenAddress common.Address
		Supported    bool
	}
	if err = json.Unmarshal(raw, &support); err != nil {
		return false, status, err
	}
	if support.TokenAddress != token {
		return false, status, errors.New("invalid broadcaster token support")
	}
	return !status.SubmissionHalted && support.Supported, status, ctx.Err()
}

// SubmissionPolicyGuard captures the initialized discovery authority for a first send.
// Manual clients still perform live submission requirements checks.
func (c *Client) SubmissionPolicyGuard() func(context.Context, func(context.Context) error) error {
	record, bound := c.preparationSnapshot()
	return func(ctx context.Context, finalCheck func(context.Context) error) error {
		// Custom validity callbacks may block. Complete them before checking the
		// wallet and network, then compare the original client receipt locally.
		if bound {
			if err := c.checkPreparation(ctx, record); err != nil {
				return err
			}
		}
		if finalCheck != nil {
			if err := finalCheck(ctx); err != nil {
				return err
			}
		}
		if bound && record.localPolicy {
			return c.checkPreparation(ctx, record)
		}
		current, initialized := c.preparationSnapshot()
		if initialized != bound || current != record {
			return errors.New("broadcaster authority changed during submission")
		}
		return ctx.Err()
	}
}
