package chain

import (
	"context"
	"encoding/json"
	"errors"
	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
)

// HTTPBinding owns a fixed HTTP endpoint and its deployment-validation lifetime.
// It retains no nonce, balance, allowance or signature-policy state. Close and
// Invalidate end the current epoch. Failed reads invalidate it too. An explicit
// JSON-RPC reply declining a supported-form probe is a capability result.
// Arbitrary externally supplied providers do not acquire this capability.
type HTTPBinding struct {
	client                        *ethclient.Client
	transport                     *http.Transport
	epoch                         atomic.Uint64
	closed                        atomic.Bool
	canonicalCodeUnsupportedEpoch atomic.Uint64
}

// NewHTTPBinding creates an owned native HTTP connection. A custom RoundTripper
// is deliberately excluded from the built-in simulation/deployment capability.
func NewHTTPBinding(ctx context.Context, endpoint string, clients ...*http.Client) (*HTTPBinding, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(clients) > 1 {
		return nil, errors.New("fixed HTTP(S) endpoint required")
	}
	h := &http.Client{}
	if len(clients) == 1 && clients[0] != nil {
		copy := *clients[0]
		h = &copy
	}
	if h.Transport == nil {
		h.Transport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	} else {
		native, ok := h.Transport.(*http.Transport)
		if !ok || native.Dial != nil || native.DialContext != nil || native.DialTLS != nil || native.DialTLSContext != nil || native.OnProxyConnectResponse != nil || native.Proxy != nil || len(native.TLSNextProto) != 0 {
			return nil, errors.New("native HTTP transport without custom hooks required")
		}
		h.Transport = native.Clone()
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c, err := gethrpc.DialOptions(ctx, endpoint, gethrpc.WithHTTPClient(h))
	if err != nil {
		return nil, err
	}
	b := &HTTPBinding{client: ethclient.NewClient(c), transport: h.Transport.(*http.Transport)}
	b.epoch.Store(1)
	return b, nil
}
func (b *HTTPBinding) Epoch() uint64 { return b.epoch.Load() }
func (b *HTTPBinding) Invalidate()   { b.epoch.Add(1) }
func (b *HTTPBinding) Close() {
	if b == nil {
		return
	}
	if b.closed.CompareAndSwap(false, true) {
		b.Invalidate()
		if b.client != nil {
			b.client.Close()
		}
		if b.transport != nil {
			b.transport.CloseIdleConnections()
		}
	}
}
func (b *HTTPBinding) check() error {
	if b == nil || b.client == nil || b.closed.Load() {
		return errors.New("HTTP binding disposed")
	}
	return nil
}

func (b *HTTPBinding) current(ctx context.Context, epoch uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.check(); err != nil {
		return err
	}
	if b.Epoch() != epoch {
		return errors.New("HTTP binding changed during request")
	}
	return nil
}
func bindingRead[T any](b *HTTPBinding, ctx context.Context, load func() (T, error)) (T, error) {
	var zero T
	if err := b.check(); err != nil {
		return zero, err
	}
	epoch := b.Epoch()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	value, err := load()
	if err != nil {
		b.Invalidate()
		return zero, err
	}
	if err = b.current(ctx, epoch); err != nil {
		return zero, err
	}
	return value, nil
}
func (b *HTTPBinding) ChainID(ctx context.Context) (*big.Int, error) {
	return bindingRead(b, ctx, func() (*big.Int, error) { return b.client.ChainID(ctx) })
}
func (b *HTTPBinding) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	return bindingRead(b, ctx, func() (*types.Header, error) { return b.client.HeaderByNumber(ctx, n) })
}
func (b *HTTPBinding) CodeAt(ctx context.Context, a common.Address, n *big.Int) ([]byte, error) {
	return bindingRead(b, ctx, func() ([]byte, error) { return b.client.CodeAt(ctx, a, n) })
}

// CodeAtCanonicalHash reads code at an EIP-1898 block hash with requireCanonical
// set. Method-not-found or an explicit selector-format rejection reports an
// unsupported selector. Ambiguous invalid-params replies remain errors.
// This negative capability lasts for this binding epoch. Successful probes
// always read live code. Unknown and noncanonical blocks remain errors.
func (b *HTTPBinding) CodeAtCanonicalHash(ctx context.Context, account common.Address, hash common.Hash) (code []byte, supported bool, err error) {
	result, err := bindingRead(b, ctx, func() (*hexutil.Bytes, error) {
		epoch := b.Epoch()
		previous := b.canonicalCodeUnsupportedEpoch.Load()
		if previous == epoch {
			return nil, nil
		}
		var result hexutil.Bytes
		selector := gethrpc.BlockNumberOrHashWithHash(hash, true)
		err := b.client.Client().CallContext(ctx, &result, "eth_getCode", account, selector)
		if unsupportedCanonicalSelector(err) {
			// Leave rejection of a retired read to bindingRead. An old probe
			// cannot publish a capability for, or invalidate, the new epoch.
			if b.current(ctx, epoch) == nil {
				b.canonicalCodeUnsupportedEpoch.CompareAndSwap(previous, epoch)
			}
			return nil, nil
		}
		return &result, err
	})
	if err != nil || result == nil {
		return nil, false, err
	}
	return *result, true, nil
}

func unsupportedCanonicalSelector(err error) bool {
	var reply gethrpc.Error
	if !errors.As(err, &reply) {
		return false
	}
	switch reply.ErrorCode() {
	case -32601:
		return true
	case -32602:
		// Nodes may also use invalid-params for an unknown or noncanonical
		// block. Only a recognized rejection of the selector shape is safe.
		switch reply.Error() {
		case "unsupported block selector", "invalid argument 1: json: cannot unmarshal object into Go value of type string":
			return true
		}
	}
	return false
}

func (b *HTTPBinding) CallContract(ctx context.Context, m ethereum.CallMsg, n *big.Int) ([]byte, error) {
	return bindingRead(b, ctx, func() ([]byte, error) { return b.client.CallContract(ctx, m, n) })
}
func (b *HTTPBinding) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	return bindingRead(b, ctx, func() (*types.Receipt, error) { return b.client.TransactionReceipt(ctx, h) })
}

type nativeTransactionLookup struct {
	transaction *types.Transaction
	pending     bool
}

// TransactionByHash preserves calldata authentication for ordinary, Credit and
// registry reconciliation without exposing the unguarded raw RPC connection.
func (b *HTTPBinding) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	value, err := bindingRead(b, ctx, func() (nativeTransactionLookup, error) {
		tx, pending, err := b.client.TransactionByHash(ctx, hash)
		return nativeTransactionLookup{transaction: tx, pending: pending}, err
	})
	return value.transaction, value.pending, err
}

// HeaderByTag preserves native finality's declared wire-hash authentication.
func (b *HTTPBinding) HeaderByTag(ctx context.Context, tag protocolrpc.FinalityTag) (*types.Header, error) {
	if !tag.Valid() {
		return nil, errors.New("invalid native finality tag")
	}
	return bindingRead(b, ctx, func() (*types.Header, error) {
		var raw json.RawMessage
		if err := b.client.Client().CallContext(ctx, &raw, "eth_getBlockByNumber", string(tag), false); err != nil {
			return nil, err
		}
		var header *types.Header
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, protocolrpc.ErrFinalityInconsistent
		}
		if header == nil {
			return nil, nil
		}
		var wire struct{ Hash common.Hash }
		if err := json.Unmarshal(raw, &wire); err != nil || wire.Hash == (common.Hash{}) || wire.Hash != header.Hash() {
			return nil, protocolrpc.ErrFinalityInconsistent
		}
		return header, nil
	})
}
