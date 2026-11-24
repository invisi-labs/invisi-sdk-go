package chain

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
)

// BroadcasterOptions filters registered operators by a live token capability probe.
type BroadcasterOptions struct {
	TokenAddress  common.Address
	ClientOptions broadcaster.Options
}

// GetBroadcasters returns enabled registered clients that advertise fees for this token.
// Unsupported and unreachable operators are skipped. Registry pagination remains pinned
// to the first page's indexed block; this is public publisher metadata, not endorsement.
// NewClient copies the client value and forces its own redirect policy. Retain
// the actual transport/jar/timeout snapshot so same-pointer policy replacement
// cannot silently keep using the earlier client. Uncomparable policies stay fresh.
type broadcasterHTTPPolicy struct {
	transport http.RoundTripper
	jar       http.CookieJar
	timeout   time.Duration
}

func captureBroadcasterHTTPPolicy(c *http.Client) (broadcasterHTTPPolicy, bool) {
	policy := broadcasterHTTPPolicy{transport: http.DefaultTransport}
	if c != nil {
		policy.jar = c.Jar
		policy.timeout = c.Timeout
		if c.Transport != nil {
			policy.transport = c.Transport
		}
	}
	for _, v := range []any{policy.transport, policy.jar} {
		if v != nil && !reflect.ValueOf(v).Comparable() {
			return broadcasterHTTPPolicy{}, false
		}
	}
	return policy, true
}
func (p broadcasterHTTPPolicy) matches(c *http.Client) bool {
	current, ok := captureBroadcasterHTTPPolicy(c)
	return ok && p.transport == current.transport && p.jar == current.jar && p.timeout == current.timeout
}

type initializedBroadcasters struct {
	revoked    atomic.Bool
	published  bool
	source     *protocolrpc.ProtocolRpcClient
	binding    *HTTPBinding
	epoch      uint64
	httpClient *http.Client
	policy     broadcasterHTTPPolicy
	clients    []*broadcaster.Client
}

// InitializeBroadcasters explicitly pays discovery and token-support probing once
// for this owned HTTP binding epoch and exact client policy. Submission service
// requirements stay live; changing the supplied client policy prevents reuse.
func (p *Protocol) InitializeBroadcasters(ctx context.Context, options BroadcasterOptions) (clients []*broadcaster.Client, err error) {
	// The receipt pointer is also the refresh ownership token. Retire the old
	// receipt immediately; only this newest successful nonempty refresh may publish.
	policy, cacheable := captureBroadcasterHTTPPolicy(options.ClientOptions.HTTPClient)
	source := p.rpc
	claim := &initializedBroadcasters{}
	p.discoveryMu.Lock()
	if p.discovery == nil {
		p.discovery = map[common.Address]*initializedBroadcasters{}
	}
	if old := p.discovery[options.TokenAddress]; old != nil {
		old.revoked.Store(true)
	}
	delete(p.discovery, options.TokenAddress)
	if len(p.discovery) >= 256 {
		for token := range p.discovery {
			p.discovery[token].revoked.Store(true)
			delete(p.discovery, token)
			break
		}
	}
	p.discovery[options.TokenAddress] = claim
	p.discoveryMu.Unlock()
	published := false
	defer func() {
		if !published {
			p.discoveryMu.Lock()
			if p.discovery[options.TokenAddress] == claim {
				delete(p.discovery, options.TokenAddress)
			}
			p.discoveryMu.Unlock()
		}
	}()
	binding, _ := p.provider.(*HTTPBinding)
	var epoch uint64
	if binding != nil {
		epoch = binding.Epoch()
		if err = p.ValidateDeployment(ctx, binding); err != nil {
			return nil, err
		}
	}
	var valid func(context.Context) error
	if binding != nil && cacheable && policy.matches(options.ClientOptions.HTTPClient) {
		valid = func(check context.Context) error {
			if e := binding.current(check, epoch); e != nil {
				claim.revoked.Store(true)
				return e
			}
			if !policy.matches(options.ClientOptions.HTTPClient) {
				claim.revoked.Store(true)
				return errors.New("broadcaster HTTP policy changed")
			}
			p.discoveryMu.Lock()
			current := p.discovery[options.TokenAddress] == claim && claim.published && p.provider == binding && p.rpc == source
			p.discoveryMu.Unlock()
			if !current || claim.revoked.Load() {
				claim.revoked.Store(true)
				return errors.New("broadcaster discovery receipt retired")
			}
			return nil
		}
	}
	clients, err = p.discoverBroadcasters(ctx, options, valid)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if binding == nil || len(clients) == 0 || !cacheable || !policy.matches(options.ClientOptions.HTTPClient) {
		return clients, nil
	}
	if err = binding.current(ctx, epoch); err != nil {
		return nil, err
	}
	p.discoveryMu.Lock()
	defer p.discoveryMu.Unlock()
	if p.discovery[options.TokenAddress] != claim || claim.revoked.Load() {
		return clients, nil
	}
	if err = binding.current(ctx, epoch); err != nil {
		return nil, err
	}
	claim.source = source
	claim.binding = binding
	claim.epoch = epoch
	claim.httpClient = options.ClientOptions.HTTPClient
	claim.policy = policy
	claim.clients = append([]*broadcaster.Client(nil), clients...)
	if err = binding.current(ctx, epoch); err != nil {
		return nil, err
	}
	claim.published = true
	published = true
	return clients, nil
}
func (p *Protocol) GetBroadcasters(ctx context.Context, options BroadcasterOptions) ([]*broadcaster.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if binding, ok := p.provider.(*HTTPBinding); ok {
		epoch := binding.Epoch()
		p.discoveryMu.Lock()
		entry, found := p.discovery[options.TokenAddress]
		var clients []*broadcaster.Client
		reusable := found && entry != nil && !entry.revoked.Load() && entry.published && len(entry.clients) > 0 && entry.binding == binding && entry.source == p.rpc && entry.epoch == epoch && entry.httpClient == options.ClientOptions.HTTPClient && entry.policy.matches(options.ClientOptions.HTTPClient)
		if reusable {
			clients = append([]*broadcaster.Client(nil), entry.clients...)
		} else if entry != nil && entry.published {
			entry.revoked.Store(true)
		}
		p.discoveryMu.Unlock()
		if binding.check() != nil {
			return nil, errors.New("HTTP binding disposed")
		}
		if reusable && binding.current(ctx, epoch) == nil {
			return clients, nil
		}
		if err := p.ValidateDeployment(ctx, binding); err != nil {
			return nil, err
		}
	}
	return p.discoverBroadcasters(ctx, options, nil)
}
func (p *Protocol) discoverBroadcasters(ctx context.Context, options BroadcasterOptions, valid func(context.Context) error) ([]*broadcaster.Client, error) {
	if p.deployment.BroadcasterRegistry == nil {
		return nil, errors.New("deployment has no broadcaster registry")
	}
	if p.rpc == nil {
		return nil, errors.New("broadcaster discovery requires ProtocolFromRPC")
	}
	if options.TokenAddress == (common.Address{}) {
		return nil, errors.New("token address required")
	}
	enabled := true
	q := protocolrpc.BroadcasterQuery{Enabled: &enabled, Limit: 100}
	clients := []*broadcaster.Client{}
	seen := map[common.Address]bool{}
	for {
		page, e := p.rpc.GetBroadcasters(ctx, p.Context(), q)
		if e != nil {
			return nil, e
		}
		if q.Block == nil {
			block := page.Block
			q.Block = &block
		}
		for _, entry := range page.Broadcasters {
			if !entry.Enabled || seen[entry.Operator] {
				continue
			}
			seen[entry.Operator] = true
			c, e := broadcaster.NewClient(broadcaster.Configuration{Endpoint: entry.Endpoint, QuoteSigner: entry.QuoteSigner, FeeRecipient: entry.FeeRecipient}, options.ClientOptions)
			if e != nil {
				continue
			}
			trust := broadcaster.Trust{ChainID: new(big.Int).SetUint64(p.Context().ChainID), Protocol: p.Context().ProtocolAddress}
			var supported bool
			if valid != nil {
				supported, e = c.InitializeTokenSupportWithLocalPolicy(ctx, options.TokenAddress, trust, valid)
			} else {
				supported, e = c.SupportsToken(ctx, options.TokenAddress, trust)
			}
			if e != nil || !supported {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			clients = append(clients, c)
		}
		if page.Next == nil {
			return clients, nil
		}
		if q.After != nil && page.Next.Big().Cmp(q.After.Big()) <= 0 {
			return nil, errors.New("nonadvancing broadcaster cursor")
		}
		cursor := *page.Next
		q.After = &cursor
	}
}
