package broadcaster

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type concurrentReply struct {
	id          uint64
	ready, done chan struct{}
}
type concurrentReplyGate struct {
	mu      sync.Mutex
	size    int
	replies []*concurrentReply
	drained chan struct{}
}

// Every phase must overlap. Responses are written in reverse arrival order;
// acknowledging each write avoids depending on goroutine scheduling or sleeps.
func (g *concurrentReplyGate) wait(ctx context.Context, id uint64) (func(), error) {
	g.mu.Lock()
	if len(g.replies) >= g.size {
		drained := g.drained
		g.mu.Unlock()
		select {
		case <-drained:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	turn := &concurrentReply{id: id, ready: make(chan struct{}), done: make(chan struct{})}
	g.replies = append(g.replies, turn)
	if len(g.replies) == g.size {
		batch := append([]*concurrentReply(nil), g.replies...)
		go func() {
			defer close(g.drained)
			for i := len(batch) - 1; i >= 0; i-- {
				close(batch[i].ready)
				select {
				case <-batch[i].done:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	g.mu.Unlock()
	select {
	case <-turn.ready:
		return func() { close(turn.done) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestOneClientConcurrentQuotesLocksSubmissionsAndStatuses(t *testing.T) {
	var fixture struct {
		Trust        struct{ ChainID, Protocol, Signer string }
		Tickets      []SignedFeeLock
		Transactions []struct {
			Data, Token, FeeAmount string
			Signed                 SignedQuote
			Status                 Status
		}
	}
	raw, err := os.ReadFile("testdata/concurrent-broadcaster.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	chain, _ := new(big.Int).SetString(fixture.Trust.ChainID, 10)
	trust := Trust{ChainID: chain, Protocol: common.HexToAddress(fixture.Trust.Protocol), Signer: common.HexToAddress(fixture.Trust.Signer)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gates := map[string]*concurrentReplyGate{}
	for method, size := range map[string]int{"broadcaster_lockFee": 4, "broadcaster_estimateQuote": 4, "broadcaster_getQuote": 4, "broadcaster_submitTransaction": 8, "broadcaster_getTransactionStatus": 4} {
		gates[method] = &concurrentReplyGate{size: size, drained: make(chan struct{})}
	}
	type requestRecord struct {
		ID     uint64
		Method string
		Body   json.RawMessage
	}
	var mu sync.Mutex
	var seen []requestRecord
	completed := map[string][]uint64{}
	var loseFirst, corruptStatus atomic.Bool
	loseFirst.Store(true)
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
		var body struct {
			Data, ID     string
			FeeLock      *SignedFeeLock
			Quote        Quote
			Signature    string
			TokenAddress common.Address
		}
		var wire json.RawMessage
		if len(req.Params) > 0 {
			wire = req.Params[0]
			if e := json.Unmarshal(wire, &body); e != nil {
				t.Error(e)
				return
			}
		}
		mu.Lock()
		seen = append(seen, requestRecord{req.ID, req.Method, wire})
		mu.Unlock()
		index := -1
		for i, tx := range fixture.Transactions {
			if body.Data == tx.Data || (req.Method == "broadcaster_getTransactionStatus" && body.ID == tx.Status.ID) {
				index = i
				break
			}
		}
		var result any
		lose := false
		switch req.Method {
		case "broadcaster_getTokenSupport":
			result = map[string]any{"apiVersion": "v1", "protocolVersion": "1", "chainId": fixture.Trust.ChainID, "protocol": trust.Protocol, "sender": trust.Signer, "feeRecipient": trust.Signer, "submissionHalted": false, "tokenAddress": body.TokenAddress, "supported": true, "feeLock": FeeLockCapabilities{Version: "1", Model: feeLockModel, Core: true}}
		case "broadcaster_lockFee", "broadcaster_estimateQuote", "broadcaster_getQuote", "broadcaster_submitTransaction":
			if index < 0 {
				t.Error("unknown calldata", req.Method)
				return
			}
			tx := fixture.Transactions[index]
			var ticket SignedFeeLock
			for _, candidate := range fixture.Tickets {
				if candidate.Lock.Token == tx.Token {
					ticket = candidate
				}
			}
			switch req.Method {
			case "broadcaster_lockFee":
				result = ticket
			case "broadcaster_estimateQuote", "broadcaster_getQuote":
				if body.FeeLock == nil || *body.FeeLock != ticket {
					t.Error("crossed pricing ticket", index)
				}
				if req.Method == "broadcaster_getQuote" {
					result = tx.Signed
				} else {
					result = Fee{FeeRecipient: trust.Signer, FeeAmount: tx.FeeAmount, MaxGas: tx.Signed.Quote.MaxGas}
				}
			case "broadcaster_submitTransaction":
				if !reflect.DeepEqual(body.Quote, tx.Signed.Quote) || body.Signature != tx.Signed.Signature || body.FeeLock != nil {
					t.Error("crossed submission packet", index)
				}
				lose = index == 0 && loseFirst.CompareAndSwap(true, false)
				result = tx.Status
			}
		case "broadcaster_getTransactionStatus":
			if index < 0 {
				t.Error("unknown status ID")
				return
			}
			result = fixture.Transactions[index].Status
			if corruptStatus.Load() {
				result = fixture.Transactions[1].Status
			}
		default:
			t.Error("unexpected RPC", req.Method)
			return
		}
		if gate := gates[req.Method]; gate != nil {
			finish, e := gate.wait(ctx, req.ID)
			if e != nil {
				return
			}
			defer finish()
		}
		mu.Lock()
		completed[req.Method] = append(completed[req.Method], req.ID)
		mu.Unlock()
		if lose {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_ = conn.Close()
			return
		}
		if e := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); e != nil {
			t.Error(e)
		}
		w.(http.Flusher).Flush()
	}))
	defer server.Close()
	client, err := NewClient(Configuration{server.URL, trust.Signer, trust.Signer}, Options{server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if ok, e := client.InitializeTokenSupport(ctx, common.HexToAddress(fixture.Transactions[0].Token), trust, func(c context.Context) error { return c.Err() }); e != nil || !ok {
		t.Fatal(ok, e)
	}
	scoped := make([]*Client, 4)
	quotes := make([]SignedQuote, 4)
	var retries atomic.Int64
	errors := make(chan error, 4)
	var wg sync.WaitGroup
	for i := range fixture.Transactions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := fixture.Transactions[i]
			data := common.FromHex(tx.Data)
			c, e := client.ForPreparation(ctx, data, trust)
			if e != nil {
				errors <- e
				return
			}
			scoped[i] = c
			fee, e := c.EstimateQuote(ctx, data)
			if e != nil {
				errors <- e
				return
			}
			if fee.FeeAmount != tx.FeeAmount {
				errors <- fmt.Errorf("crossed estimate %d", i)
				return
			}
			quote, e := c.GetQuote(ctx, data, trust)
			if e != nil {
				errors <- e
				return
			}
			quotes[i] = quote
			if !reflect.DeepEqual(quote.Quote, tx.Signed.Quote) {
				errors <- fmt.Errorf("crossed quote %d", i)
				return
			}
			type attempt struct {
				status Status
				err    error
			}
			attempts := make(chan attempt, 2)
			for j := 0; j < 2; j++ {
				go func() { status, e := client.SubmitTransaction(ctx, data, quote); attempts <- attempt{status, e} }()
			}
			for j := 0; j < 2; j++ {
				a := <-attempts
				if a.err != nil {
					retries.Add(1)
					a.status, a.err = client.SubmitTransaction(ctx, data, quote)
				}
				if a.err != nil {
					errors <- a.err
					return
				}
				if !reflect.DeepEqual(a.status, tx.Status) {
					errors <- fmt.Errorf("crossed submission status %d", i)
					return
				}
			}
			status, e := client.Status(ctx, tx.Status.ID)
			if e != nil {
				errors <- e
				return
			}
			if !reflect.DeepEqual(status, tx.Status) {
				errors <- fmt.Errorf("crossed polled status %d", i)
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		t.Error(e)
	}
	if t.Failed() {
		for name, gate := range gates {
			gate.mu.Lock()
			t.Log(name, len(gate.replies))
			gate.mu.Unlock()
		}
		mu.Lock()
		for _, req := range seen {
			t.Log(req.Method, req.ID)
		}
		mu.Unlock()
		return
	}
	if retries.Load() != 1 {
		t.Fatal("unexpected lost-response retries", retries.Load())
	}
	mu.Lock()
	records := append([]requestRecord(nil), seen...)
	mu.Unlock()
	ids := map[uint64]bool{}
	for _, req := range records {
		if req.Method == "broadcaster_estimateQuote" || req.Method == "broadcaster_getQuote" {
			continue
		}
		if ids[req.ID] {
			t.Fatal("reused original-client request ID", req.ID)
		}
		ids[req.ID] = true
	}
	for method, gate := range gates {
		var arrival []uint64
		gate.mu.Lock()
		for _, reply := range gate.replies {
			arrival = append(arrival, reply.id)
		}
		gate.mu.Unlock()
		mu.Lock()
		returned := append([]uint64(nil), completed[method]...)
		mu.Unlock()
		if len(arrival) != gate.size || len(returned) < gate.size {
			t.Fatal("phase did not overlap", method)
		}
		for i, id := range arrival {
			if returned[gate.size-1-i] != id {
				t.Fatal("phase did not return out of order", method, arrival, returned)
			}
		}
	}
	for i, tx := range fixture.Transactions {
		var bodies []json.RawMessage
		for _, req := range records {
			if req.Method != "broadcaster_submitTransaction" {
				continue
			}
			var p struct{ Data string }
			_ = json.Unmarshal(req.Body, &p)
			if p.Data == tx.Data {
				bodies = append(bodies, req.Body)
			}
		}
		want := 2
		if i == 0 {
			want = 3
		}
		if len(bodies) != want {
			t.Fatal("wrong submission retry count", i, len(bodies))
		}
		for _, body := range bodies {
			if !reflect.DeepEqual(body, bodies[0]) {
				t.Fatal("retry changed frozen packet", i)
			}
		}
	}
	before := len(records)
	if _, e := client.SubmitTransaction(ctx, common.FromHex(fixture.Transactions[0].Data), quotes[2]); e == nil {
		t.Fatal("crossed quote accepted")
	}
	mu.Lock()
	after := len(seen)
	mu.Unlock()
	if before != after {
		t.Fatal("crossed quote reached transport")
	}
	corruptStatus.Store(true)
	if _, e := client.Status(ctx, fixture.Transactions[0].Status.ID); e == nil {
		t.Fatal("crossed status ID accepted")
	}
	corruptStatus.Store(false)
	if status, e := client.Status(ctx, fixture.Transactions[0].Status.ID); e != nil || status.ID != fixture.Transactions[0].Status.ID {
		t.Fatal("bad response poisoned next status", status, e)
	}
	if scoped[0].feeLock == scoped[2].feeLock {
		t.Fatal("scoped ticket alias")
	}
	scoped[0].feeLock.Lock.GasPrice = "999"
	if scoped[2].feeLock.Lock.GasPrice != fixture.Tickets[0].Lock.GasPrice {
		t.Fatal("tariff mutation leaked to another operation")
	}
}
