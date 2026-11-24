package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

type reuseFixture struct {
	client    *Client
	data      []byte
	trust     Trust
	ticket    SignedFeeLock
	quote     SignedQuote
	locks     atomic.Int64
	revoked   atomic.Bool
	rejection atomic.Pointer[RPCError]
	estimated atomic.Pointer[SignedFeeLock]
	quoted    atomic.Pointer[SignedFeeLock]
	hold      atomic.Bool
	failNext  atomic.Bool
	entered   chan chan struct{}
}

func newReuseFixture(t *testing.T) *reuseFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/fee-lock-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SignedFeeLock
		Data  string
		Quote SignedQuote
		Trust struct{ ChainID, Protocol, Signer string }
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	chain, _ := new(big.Int).SetString(v.Trust.ChainID, 10)
	f := &reuseFixture{data: common.FromHex(v.Data), trust: Trust{ChainID: chain, Protocol: common.HexToAddress(v.Trust.Protocol), Signer: common.HexToAddress(v.Trust.Signer)}, ticket: v.SignedFeeLock, quote: v.Quote, entered: make(chan chan struct{}, 1)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		var result any
		switch req.Method {
		case "broadcaster_getTokenSupport", "broadcaster_getStatus":
			result = map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": f.trust.ChainID.String(), "protocol": f.trust.Protocol, "sender": f.trust.Signer, "feeRecipient": f.ticket.Lock.FeeRecipient, "tokenAddress": f.ticket.Lock.Token, "supported": true, "feeLock": FeeLockCapabilities{Version: "1", Model: feeLockModel, Core: true}}
		case "broadcaster_lockFee":
			count := f.locks.Add(1)
			fail := f.failNext.Swap(false)
			var params struct{ Data string }
			_ = json.Unmarshal(req.Params[0], &params)
			intent, e := DecodeIntent(common.FromHex(params.Data))
			if e != nil {
				t.Error(e)
				return
			}
			ticket := f.ticket
			ticket.Lock.Token = intent.Token.Hex()
			ticket.Lock.GasPrice = strconv.FormatInt(count, 10)
			ticket = f.sign(t, ticket)
			if f.hold.Swap(false) {
				release := make(chan struct{})
				f.entered <- release
				<-release
			}
			if fail {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": &RPCError{-32041, "FEE_LOCK_INVALID"}})
				return
			}
			result = ticket
		case "broadcaster_estimateQuote", "broadcaster_getQuote":
			if reject := f.rejection.Load(); reject != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": reject})
				return
			}
			var params struct{ FeeLock *SignedFeeLock }
			_ = json.Unmarshal(req.Params[0], &params)
			if params.FeeLock == nil {
				t.Error("pricing downgraded")
			}
			if req.Method == "broadcaster_getQuote" {
				f.quoted.Store(params.FeeLock)
				result = f.quote
			} else {
				f.estimated.Store(params.FeeLock)
				result = Fee{FeeRecipient: common.HexToAddress(f.ticket.Lock.FeeRecipient), FeeAmount: "2", MaxGas: "100000"}
			}
		default:
			t.Error("unexpected method", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	f.client, err = NewClient(Configuration{server.URL, f.trust.Signer, common.HexToAddress(f.ticket.Lock.FeeRecipient)}, Options{server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	f.initialize(t)
	return f
}
func (f *reuseFixture) sign(t *testing.T, ticket SignedFeeLock) SignedFeeLock {
	t.Helper()
	key, err := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := FeeLockDigest(settlement.QuoteDomain{ChainID: f.trust.ChainID, Protocol: f.trust.Protocol}, ticket.Lock)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	ticket.Signature = hexutil.Encode(sig)
	return ticket
}
func (f *reuseFixture) initialize(t *testing.T) {
	t.Helper()
	ok, err := f.client.InitializeTokenSupport(context.Background(), common.HexToAddress(f.ticket.Lock.Token), f.trust, func(ctx context.Context) error {
		if f.revoked.Load() {
			return errors.New("receipt revoked")
		}
		return ctx.Err()
	})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
}
func (f *reuseFixture) prepare(t *testing.T, data []byte) *Client {
	t.Helper()
	c, err := f.client.ForPreparation(context.Background(), data, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (f *reuseFixture) wantLocks(t *testing.T, n int64) {
	t.Helper()
	if got := f.locks.Load(); got != n {
		t.Fatalf("lock calls=%d want=%d", got, n)
	}
}

func TestInitializedFeeLockReuseExpiryTokenAndScopedIsolation(t *testing.T) {
	f := newReuseFixture(t)
	first := f.prepare(t, f.data)
	second := f.prepare(t, f.data)
	f.wantLocks(t, 1)
	if first.feeLock == second.feeLock || first.feeLockTrust == second.feeLockTrust {
		t.Fatal("scoped values share mutable memory")
	}
	if _, err := second.EstimateQuote(context.Background(), f.data); err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetQuote(context.Background(), f.data, f.trust); err != nil {
		t.Fatal(err)
	}
	if f.estimated.Load() == nil || f.quoted.Load() == nil || *f.estimated.Load() != *second.feeLock || *f.quoted.Load() != *second.feeLock {
		t.Fatal("estimate and quote did not carry the same frozen ticket")
	}
	// Changing the caller's chain integer cannot mutate the frozen trust.
	before := new(big.Int).Set(second.feeLockTrust.ChainID)
	f.trust.ChainID.Add(f.trust.ChainID, big.NewInt(1))
	if second.feeLockTrust.ChainID.Cmp(before) != 0 {
		t.Fatal("trust alias")
	}
	f.trust.ChainID.Set(before)
	first.feeLock.Lock.GasPrice = "999"
	if _, err := first.EstimateQuote(context.Background(), f.data); err == nil {
		t.Fatal("tampered scoped tariff accepted")
	}
	third := f.prepare(t, f.data)
	f.wantLocks(t, 2)
	if third.feeLock.Lock.GasPrice != "2" {
		t.Fatal("tariff mutation leaked")
	}
	// Simulate time advancing past a correctly signed cached deadline.
	a := f.client.preparation
	a.mu.Lock()
	expired := *a.ticket
	expired.ticket.Lock.Deadline = strconv.FormatInt(time.Now().Unix()-1, 10)
	expired.ticket = f.sign(t, expired.ticket)
	a.ticket = &expired
	a.mu.Unlock()
	f.prepare(t, f.data)
	f.wantLocks(t, 3)
	token := common.HexToAddress(f.ticket.Lock.Token)
	other := common.HexToAddress("0x44")
	changed := bytes.Replace(f.data, common.LeftPadBytes(token.Bytes(), 32), common.LeftPadBytes(other.Bytes(), 32), 1)
	otherClient := f.prepare(t, changed)
	f.wantLocks(t, 4)
	if otherClient.feeLock.Lock.Token != other.Hex() {
		t.Fatal("token binding reused")
	}
	f.prepare(t, f.data)
	f.wantLocks(t, 5)
}

func TestFeeLockRejectionsEvictOnlyMatchingTicket(t *testing.T) {
	for _, reject := range []RPCError{{-32040, "FEE_LOCK_EXPIRED"}, {-32041, "FEE_LOCK_INVALID"}, {-32042, "FEE_LOCK_GAS_EXCEEDED"}} {
		t.Run(reject.Message, func(t *testing.T) {
			f := newReuseFixture(t)
			old := f.prepare(t, f.data)
			f.rejection.Store(&reject)
			if _, err := old.EstimateQuote(context.Background(), f.data); err == nil {
				t.Fatal("rejection ignored")
			}
			f.rejection.Store(nil)
			fresh := f.prepare(t, f.data)
			f.wantLocks(t, 2)
			f.rejection.Store(&reject)
			if _, err := old.GetQuote(context.Background(), f.data, f.trust); err == nil {
				t.Fatal("old rejection ignored")
			}
			f.rejection.Store(nil)
			again := f.prepare(t, f.data)
			f.wantLocks(t, 2)
			if *again.feeLock != *fresh.feeLock {
				t.Fatal("stale rejection evicted newer tariff")
			}
		})
	}
	f := newReuseFixture(t)
	scoped := f.prepare(t, f.data)
	for _, reject := range []RPCError{{-32000, "unavailable"}, {-32040, "other"}, {-32000, "FEE_LOCK_EXPIRED"}} {
		f.rejection.Store(&reject)
		if _, err := scoped.EstimateQuote(context.Background(), f.data); err == nil {
			t.Fatal("error ignored")
		}
		f.rejection.Store(nil)
		f.prepare(t, f.data)
		f.wantLocks(t, 1)
	}
}

func TestFeeLockOldFetchCannotPublishOverNewerTicketOrAuthority(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(strconv.FormatBool(refresh), func(t *testing.T) {
			f := newReuseFixture(t)
			f.hold.Store(true)
			done := make(chan error, 1)
			go func() { _, err := f.client.ForPreparation(context.Background(), f.data, f.trust); done <- err }()
			release := <-f.entered
			if refresh {
				f.initialize(t)
			}
			fresh := f.prepare(t, f.data)
			f.wantLocks(t, 2)
			close(release)
			err := <-done
			if refresh && err == nil {
				t.Fatal("stale authority fetch succeeded")
			}
			if !refresh && err != nil {
				t.Fatal(err)
			}
			again := f.prepare(t, f.data)
			f.wantLocks(t, 2)
			if *again.feeLock != *fresh.feeLock {
				t.Fatal("older response replaced new tariff")
			}
		})
	}
}

func TestFeeLockReceiptRevocationAndReinitializationClearTicket(t *testing.T) {
	f := newReuseFixture(t)
	old := f.prepare(t, f.data)
	f.initialize(t)
	f.prepare(t, f.data)
	f.wantLocks(t, 2)
	if _, err := old.EstimateQuote(context.Background(), f.data); err == nil {
		t.Fatal("old scoped authority revived")
	}
	f.revoked.Store(true)
	if _, err := f.client.ForPreparation(context.Background(), f.data, f.trust); err == nil {
		t.Fatal("revoked guard accepted")
	}
	f.revoked.Store(false)
	if _, err := f.client.ForPreparation(context.Background(), f.data, f.trust); err == nil {
		t.Fatal("revoked authority revived")
	}
	f.initialize(t)
	f.prepare(t, f.data)
	f.wantLocks(t, 3)
}

func TestFeeLockCachedSignatureAndEveryScopedUseRevalidate(t *testing.T) {
	f := newReuseFixture(t)
	scoped := f.prepare(t, f.data)
	// Cache admission is insufficient: a locally invalid tariff cannot be used.
	scoped.feeLock.Lock.Deadline = strconv.FormatInt(time.Now().Unix()-1, 10)
	*scoped.feeLock = f.sign(t, *scoped.feeLock)
	if _, err := scoped.EstimateQuote(context.Background(), f.data); err == nil {
		t.Fatal("expired scoped ticket used")
	}
	f.wantLocks(t, 1)
	f.prepare(t, f.data)
	f.wantLocks(t, 2)
	// A subsequent use of the stale scope must not evict the replacement.
	if _, err := scoped.GetQuote(context.Background(), f.data, f.trust); err == nil {
		t.Fatal("expired scoped ticket used for quote")
	}
	f.prepare(t, f.data)
	f.wantLocks(t, 2)
	// Corruption of stored state also cannot bypass authentication on a hit.
	a := f.client.preparation
	a.mu.Lock()
	corrupt := *a.ticket
	corrupt.ticket.Lock.Numerator = "1"
	a.ticket = &corrupt
	a.mu.Unlock()
	f.prepare(t, f.data)
	f.wantLocks(t, 3)
}

func TestFeeLockBindingChangesRequireExactAuthority(t *testing.T) {
	f := newReuseFixture(t)
	f.prepare(t, f.data)
	for _, kind := range []string{"chain", "protocol", "signer"} {
		wrong := f.trust
		switch kind {
		case "chain":
			wrong.ChainID = new(big.Int).Add(f.trust.ChainID, big.NewInt(1))
		case "protocol":
			wrong.Protocol = common.HexToAddress("0x99")
		case "signer":
			wrong.Signer = common.HexToAddress("0x99")
		}
		if _, err := f.client.ForPreparation(context.Background(), f.data, wrong); err == nil {
			t.Fatal("wrong binding reused", kind)
		}
		f.prepare(t, f.data)
		f.wantLocks(t, 1)
	}
	// Explicit reinitialization for a different deployment has a cold tariff fetch.
	f.trust.ChainID = new(big.Int).Add(f.trust.ChainID, big.NewInt(1))
	f.trust.Protocol = common.HexToAddress("0x88")
	f.initialize(t)
	f.prepare(t, f.data)
	f.wantLocks(t, 2)
}

func TestFeeLockUninitializedPreparationsRemainFresh(t *testing.T) {
	f := newReuseFixture(t)
	// A separate original client has never initialized a capability receipt.
	client, err := NewClient(f.client.configuration, Options{f.client.http})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		scoped, err := client.ForPreparation(context.Background(), f.data, f.trust)
		if err != nil {
			t.Fatal(err)
		}
		if scoped.feeLockEntry != nil {
			t.Fatal("uninitialized ticket retained")
		}
	}
	f.wantLocks(t, 2)
}

func TestFeeLockOldFetchFailureDoesNotEvictNewTicket(t *testing.T) {
	f := newReuseFixture(t)
	f.hold.Store(true)
	f.failNext.Store(true)
	done := make(chan error, 1)
	go func() { _, err := f.client.ForPreparation(context.Background(), f.data, f.trust); done <- err }()
	release := <-f.entered
	fresh := f.prepare(t, f.data)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old fetch failure missing")
	}
	again := f.prepare(t, f.data)
	f.wantLocks(t, 2)
	if *fresh.feeLock != *again.feeLock {
		t.Fatal("failed older fetch evicted new ticket")
	}
}
