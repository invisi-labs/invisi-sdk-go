package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func (s *recoveryServiceFixture) SubmitSourceProof(context.Context, any, []ProofSidecar) (Result, error) {
	return nil, errors.New("direct fixture has no historical tracking submission")
}

type historicalRecoveryHost struct {
	*recoveryHostFixture
	reproofs int
}

func (h *historicalRecoveryHost) LocalReprove(ctx context.Context, record RecoveryRecord, generation string) (ProofSidecar, error) {
	h.reproofs++
	return h.recoveryHostFixture.LocalReprove(ctx, record, generation)
}

// Keep the real public client in this regression: preflight rejects an expired
// native deadline, whereas historical delivery authenticates the mined payment.
func TestRecoveryHistoricalDeliveryHTTP(t *testing.T) {
	for _, scenario := range []string{"recognized", "pending_tracker", "tracking_outage", "recognition_outage", "cas_conflict", "initial_expired", "submit_failure", "missing_tracker", "wrong_digest", "wrong_ack_locator", "missing_locator", "bad_locator", "pending", "unseen", "reorged", "no_reproof", "direct"} {
		t.Run(scenario, func(t *testing.T) {
			c, h, service, v := setupRecovery(t)
			host := &historicalRecoveryHost{recoveryHostFixture: h}
			c.Host = host
			pollFailed := false
			var concurrentRecord RecoveryRecord
			ctx := context.Background()
			expired := scenario == "initial_expired"
			var methods []string
			digest, _ := word(v.Intent.OperationDigest)
			tracking := Tracking{ID: strings.Repeat("a", 48), Status: "pending_confirmation", ExpiresAt: "1", OperationDigest: digest}
			client := &Client{Endpoint: "https://example.test", Trust: c.Trust, HTTP: &http.Client{Transport: reservedKeyRoundTripper(func(req *http.Request) (*http.Response, error) {
				var request struct {
					ID     uint64                       `json:"id"`
					Method string                       `json:"method"`
					Params []map[string]json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					return nil, err
				}
				methods = append(methods, request.Method)
				result := Result{}
				var failure any
				switch request.Method {
				case "provenance_evaluateTransaction", "provenance_submitSourceProof":
					var data string
					var sidecars []ProofSidecar
					json.Unmarshal(request.Params[0]["data"], &data)
					json.Unmarshal(request.Params[0]["proofSidecars"], &sidecars)
					if data != v.Intent.Calldata || len(sidecars) != 1 {
						t.Error("changed frozen submission")
					}
					if expired && request.Method == "provenance_evaluateTransaction" {
						failure = map[string]any{"message": "native deadline expired", "data": map[string]any{"code": "INVALID_ARGUMENT"}}
						break
					}
					if request.Method == "provenance_submitSourceProof" {
						if scenario == "submit_failure" {
							return nil, errors.New("delivery outage")
						}
						expectedBundle, _ := ProofBundleToWire(v.Sidecars[1].ProofBundle)
						if len(sidecars) == 1 && !reflect.DeepEqual(sidecars[0], ProofSidecar{OperationIndex: "0", ProofBundle: expectedBundle}) {
							t.Error("historical submission omitted replacement proof")
						}
						if scenario == "cas_conflict" {
							current, _, loadErr := c.Store.Load(ctx, "historical")
							if loadErr != nil {
								return nil, loadErr
							}
							concurrentRecord = recoveryClone(current)
							concurrentRecord.State = "pending"
							concurrentRecord.Revision++
							ok, saveErr := c.Store.CompareAndSwap(ctx, "historical", current.Revision, concurrentRecord)
							if saveErr != nil || !ok {
								t.Fatal("concurrent worker save failed", saveErr)
							}
						}
						tracking.ID = strings.Repeat("b", 48)
						tracking.Status = "recognized"
						tracking.Matched = &TrackingMatch{TxHash: h.fact.TransactionHash, BlockHash: h.fact.Locator["blockHash"], OperationIndex: "0"}
						if scenario == "wrong_ack_locator" {
							tracking.Matched.TxHash = "0x" + strings.Repeat("ef", 32)
						}
						if scenario == "pending_tracker" {
							tracking.Status = "pending_confirmation"
							tracking.Matched = nil
						}
					}
					result["broadcastDecision"] = json.RawMessage(`"allow"`)
					result["operationDigest"], _ = json.Marshal(v.Intent.OperationDigest)
					result["tracking"], _ = json.Marshal(tracking)
					if expired && scenario == "missing_tracker" {
						delete(result, "tracking")
					}
					if expired && scenario == "wrong_digest" {
						result["operationDigest"] = json.RawMessage(`"5"`)
					}
				case "provenance_getTracking":
					var requestedID string
					json.Unmarshal(request.Params[0]["trackingId"], &requestedID)
					if requestedID != tracking.ID {
						t.Error("polled wrong tracker", requestedID)
					}
					if scenario == "tracking_outage" && !pollFailed {
						pollFailed = true
						return nil, errors.New("tracking timeout")
					}
					result["tracking"], _ = json.Marshal(tracking)
				case "provenance_getOperationRecognition":
					if scenario == "recognition_outage" && !pollFailed {
						pollFailed = true
						return nil, errors.New("recognition timeout")
					}
					locator := map[string]any{}
					for k, val := range h.fact.Locator {
						locator[k] = val
					}
					service.recognition = "recognized"
					result, _ = service.GetOperationRecognition(ctx, locator)
				default:
					t.Errorf("unexpected payment/proof submission %s", request.Method)
				}
				for key, val := range map[string]any{"scope": v.Scope, "providerId": v.ProviderID, "chainGeneration": c.Trust.ChainGeneration, "signingKeyId": v.SigningKeyID, "manifestSha256": v.Manifest} {
					result[key], _ = json.Marshal(val)
				}
				envelope := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
				if failure != nil {
					delete(envelope, "result")
					envelope["error"] = failure
				}
				body, _ := json.Marshal(envelope)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})}}
			c.Service = client
			var prepared RecoveryRecord
			var err error
			if scenario == "direct" {
				prepared, err = c.PrepareDirect(ctx, "historical", v.Intent, v.Sidecars[0], "stable")
			} else {
				prepared, err = c.Prepare(ctx, "historical", v.Intent, v.Sidecars[0], "stable")
			}
			if scenario == "initial_expired" {
				if err == nil {
					t.Fatal("expired initial preparation got permit")
				}
				if _, ok, _ := c.Store.Load(ctx, "historical"); ok {
					t.Fatal("failed preparation persisted")
				}
				if !reflect.DeepEqual(methods, []string{"provenance_evaluateTransaction"}) {
					t.Fatal(methods)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expired = true
			c.Trust.ChainGeneration = "2"
			client.Trust = c.Trust
			h.fact.ChainGeneration = "2"
			switch scenario {
			case "pending", "unseen", "reorged":
				h.fact.Status = scenario
			case "missing_locator":
				h.fact.Locator = nil
			case "bad_locator":
				h.fact.Locator["blockHash"] = "bad"
			}
			methods = nil
			if scenario == "direct" {
				c.Service = service
				service.recognition = "recognized"
			}
			got, err := c.Resume(ctx, "historical", scenario != "no_reproof")
			saved, _, _ := c.Store.Load(ctx, "historical")
			switch scenario {
			case "tracking_outage", "recognition_outage":
				if err == nil {
					t.Fatal("polling outage was ignored")
				}
				if saved.State != "pending" || saved.Revision != prepared.Revision+1 || saved.TrackingID != strings.Repeat("b", 48) || saved.ChainGeneration != "2" || !reflect.DeepEqual(saved.Sidecar, v.Sidecars[1]) || !reflect.DeepEqual(saved.Locator, h.fact.Locator) || saved.TransactionHash != h.fact.TransactionHash {
					t.Fatal("acknowledged historical delivery was not durable before polling", saved.State, saved.Revision, saved.TrackingID, saved.ChainGeneration)
				}
				if saved.DeliveryMode != prepared.DeliveryMode || saved.IdempotencyKey != prepared.IdempotencyKey || !reflect.DeepEqual(saved.Intent, prepared.Intent) {
					t.Fatal("frozen recovery identity changed")
				}
				if host.reproofs != 1 {
					t.Fatal("unexpected reproof count", host.reproofs)
				}
				methods = nil
				restarted := *c
				resumed, resumeErr := restarted.Resume(ctx, "historical", false)
				if resumeErr != nil || resumed.State != "recognized" || resumed.TrackingID != saved.TrackingID || host.reproofs != 1 || !reflect.DeepEqual(methods, []string{"provenance_getTracking", "provenance_getOperationRecognition"}) {
					t.Fatal("restart failed to resume durable historical delivery", resumed.State, resumeErr, methods, host.reproofs)
				}
			case "cas_conflict":
				if err == nil || !strings.Contains(err.Error(), "CAS conflict") {
					t.Fatal("competing worker did not fence recovery", err)
				}
				if !reflect.DeepEqual(saved, concurrentRecord) {
					t.Fatal("concurrent worker state overwritten")
				}
				if !reflect.DeepEqual(methods, []string{"provenance_submitSourceProof"}) {
					t.Fatal("poll occurred before acknowledgment persisted", methods)
				}
			case "submit_failure", "missing_tracker", "wrong_digest", "wrong_ack_locator", "missing_locator", "bad_locator":
				if err == nil {
					t.Fatal("invalid recovery succeeded")
				}
				if !reflect.DeepEqual(saved, prepared) {
					t.Fatal("failed recovery wrote store")
				}
				if scenario == "wrong_ack_locator" && !reflect.DeepEqual(methods, []string{"provenance_submitSourceProof"}) {
					t.Fatal("polled after invalid acknowledgment", methods)
				}
				if scenario == "missing_locator" || scenario == "bad_locator" {
					if len(methods) != 0 {
						t.Fatal("invalid final fact delivered", methods)
					}
				}
			case "pending", "unseen", "reorged", "no_reproof":
				if err != nil || got.State != "needs_reproof" || len(methods) != 0 {
					t.Fatal(got.State, err, methods)
				}
			case "direct":
				if err != nil || got.State != "recognized" || service.submits != 1 || got.TrackingID != "" {
					t.Fatal(got.State, err)
				}
			default:
				wantState := "recognized"
				wantMethods := []string{"provenance_submitSourceProof", "provenance_getTracking", "provenance_getOperationRecognition"}
				if scenario == "pending_tracker" {
					wantState = "pending"
					wantMethods = wantMethods[:2]
				}
				if err != nil || got.State != wantState {
					t.Fatal(got.State, err, methods)
				}
				if !reflect.DeepEqual(methods, wantMethods) {
					t.Fatal(methods)
				}
				if got.TrackingID != strings.Repeat("b", 48) || got.DeliveryMode != "tracked" || got.IdempotencyKey != "stable" || !reflect.DeepEqual(got.Intent, prepared.Intent) {
					t.Fatal("frozen recovery identity changed")
				}
				methods = nil
				restarted := *c
				resumed, resumeErr := restarted.Resume(ctx, "historical", false)
				if resumeErr != nil || resumed.TrackingID != got.TrackingID || resumed.State != got.State || !reflect.DeepEqual(methods, []string{"provenance_getTracking"}) {
					t.Fatal("restart failed to reuse historical tracker", resumed.State, resumeErr, methods)
				}
			}
		})
	}
}
