package session

import (
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-sdk-go/rpc/chain"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOwnedHTTPBindingReconcilesConfirmedReceiptWithAuthenticatedCalldata(t *testing.T) {
	owner, op, provider := confirmingFixture(t)
	provider.receipt.Logs = []*types.Log{}
	var lookups atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage
			Method string
			Params []json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		var result any
		var err error
		switch req.Method {
		case "eth_chainId":
			var n *big.Int
			n, err = provider.ChainID(r.Context())
			result = hexutil.EncodeBig(n)
		case "eth_getCode":
			var addr common.Address
			json.Unmarshal(req.Params[0], &addr)
			var code []byte
			code, err = provider.CodeAt(r.Context(), addr, nil)
			result = hexutil.Encode(code)
		case "eth_call":
			var call struct {
				To    common.Address
				Input hexutil.Bytes
				Data  hexutil.Bytes
			}
			json.Unmarshal(req.Params[0], &call)
			data := call.Input
			if len(data) == 0 {
				data = call.Data
			}
			var out []byte
			out, err = provider.CallContract(r.Context(), ethereum.CallMsg{To: &call.To, Data: data}, nil)
			result = hexutil.Encode(out)
		case "eth_getBlockByNumber":
			var tag string
			json.Unmarshal(req.Params[0], &tag)
			var number *big.Int
			if tag != "latest" {
				number, err = hexutil.DecodeBig(tag)
			}
			if err == nil {
				result, err = provider.HeaderByNumber(r.Context(), number)
			}
		case "eth_getTransactionReceipt":
			result = provider.receipt
		case "eth_getTransactionByHash":
			lookups.Add(1)
			raw, _ := json.Marshal(provider.transaction)
			var tx map[string]any
			json.Unmarshal(raw, &tx)
			tx["blockNumber"] = hexutil.EncodeBig(provider.receipt.BlockNumber)
			tx["blockHash"] = provider.receipt.BlockHash
			result = tx
		default:
			t.Errorf("unexpected native recovery RPC %s", req.Method)
			err = context.Canceled
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if err != nil {
			reply = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": err.Error()}}
		}
		json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	binding, err := chain.NewHTTPBinding(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	owner.options.Provider = binding
	owner.rpc = owner.rpc.WithProvider(binding)
	resolved, err := op.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status() != "confirmed" || lookups.Load() != 1 {
		t.Fatalf("status=%s transaction lookups=%d", resolved.Status(), lookups.Load())
	}
	state, err := owner.options.Store.ReadSnapshot(context.Background(), owner.scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range op.plan.Inputs {
		n := state.Notes[input.Commitment]
		if !n.Spent || n.ReservedBy != "" {
			t.Fatal("confirmed native binding retained spend reservation")
		}
	}
}
