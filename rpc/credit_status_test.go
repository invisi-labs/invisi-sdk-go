package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestIndexedCreditStatusIdentitiesAndCanonicalAnchor(t *testing.T) {
	for _, scenario := range []string{"normal", "max_uint128", "zero_token", "zero_owner", "nil_id", "zero_id", "negative_id", "overflow_id", "wrong_scope", "wrong_block", "noncanonical", "bad_status"} {
		t.Run(scenario, func(t *testing.T) {
			provider := testProvider{&types.Header{Number: big.NewInt(10), Time: 123, ParentHash: common.HexToHash("0x09")}}
			block := CanonicalBlock{Number: 10, Hash: provider.header.Hash(), ParentHash: provider.header.ParentHash, Timestamp: 123}
			deployment := ProtocolDeployment{ChainID: "31337", Protocol: ContractDeployment{common.HexToAddress("0x1111111111111111111111111111111111111111"), crypto.Keccak256Hash([]byte{0x60, 0}), 1}}
			owner := common.HexToAddress("0x2222222222222222222222222222222222222222")
			token := common.HexToAddress("0x3333333333333333333333333333333333333333")
			id := big.NewInt(7)
			scope := DeploymentContext{31337, deployment.Protocol.Address}
			switch scenario {
			case "max_uint128":
				id, _ = new(big.Int).SetString("340282366920938463463374607431768211455", 10)
			case "zero_token":
				token = common.Address{}
			case "zero_owner":
				owner = common.Address{}
			case "nil_id":
				id = nil
			case "zero_id":
				id = big.NewInt(0)
			case "negative_id":
				id = big.NewInt(-1)
			case "overflow_id":
				id = new(big.Int).Lsh(big.NewInt(1), 128)
			case "wrong_scope":
				scope.ProtocolAddress = owner
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     uint64
					Method string
					Params []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				var result any
				switch request.Method {
				case "invisi_getStatus":
					result = map[string]any{"apiVersion": "v1", "protocolVersion": "1", "deployment": deployment, "indexedHead": block}
				case "invisi_getCreditStatus":
					calls++
					var params []any
					raw, _ := json.Marshal(request.Params)
					json.Unmarshal(raw, &params)
					want := []any{owner.Hex(), token.Hex(), id.String(), float64(10)}
					// Addresses serialize in lowercase; compare their decoded address values.
					if len(params) != 4 || common.HexToAddress(params[0].(string)) != owner || common.HexToAddress(params[1].(string)) != token || !reflect.DeepEqual(params[2:], want[2:]) {
						t.Errorf("wrong indexed query: %v", params)
					}
					claimed := block
					status := 2
					if scenario == "wrong_block" {
						claimed.Timestamp++
					}
					if scenario == "bad_status" {
						status = 3
					}
					result = map[string]any{"block": claimed, "status": status}
					if scenario == "noncanonical" {
						provider.header.Time++
					}
				default:
					t.Error("unexpected method", request.Method)
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer server.Close()
			client, err := NewProtocolRpcClient(server.URL+"/v1", ClientOptions{Deployment: &deployment})
			if err != nil {
				t.Fatal(err)
			}
			client = client.WithProvider(provider)
			got, err := client.GetCreditStatus(context.Background(), scope, owner, token, id, block)
			valid := scenario == "normal" || scenario == "max_uint128" || scenario == "zero_token"
			if valid {
				if err != nil || got.Status != 2 || got.Block != block || calls != 1 {
					t.Fatal(got, err, calls)
				}
			} else if err == nil {
				t.Fatal("invalid identity/anchor accepted")
			}
			if scenario == "zero_owner" || scenario == "nil_id" || scenario == "zero_id" || scenario == "negative_id" || scenario == "overflow_id" || scenario == "wrong_scope" {
				if calls != 0 {
					t.Fatal("invalid identity sent")
				}
			}
		})
	}
}
