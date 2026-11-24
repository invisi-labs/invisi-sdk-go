package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"

	"github.com/invisi-labs/invisi-sdk-go/rpc/broadcaster"
	"github.com/invisi-labs/invisi-sdk-go/sdk"
)

type sourceStageRequest struct {
	Service string          `json:"service"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type sourceStageSample struct {
	Stage        string               `json:"stage"`
	Requests     []sourceStageRequest `json:"requests"`
	MainProves   int                  `json:"mainProves"`
	SourceProves int64                `json:"sourceProves"`
	ElapsedMS    float64              `json:"elapsedMs"`
}
type sourceTrace struct {
	mu         sync.Mutex
	requests   []sourceStageRequest
	dropSubmit bool
	delay      time.Duration
}
type sourceStageTransport struct {
	service string
	base    http.RoundTripper
	trace   *sourceTrace
}

func (tr sourceStageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var captured sourceStageRequest
	if err = json.Unmarshal(body, &captured); err != nil {
		return nil, err
	}
	captured.Service = tr.service
	tr.trace.mu.Lock()
	tr.trace.requests = append(tr.trace.requests, captured)
	drop := tr.trace.dropSubmit && captured.Method == "broadcaster_submitTransaction"
	tr.trace.mu.Unlock()
	if tr.trace.delay > 0 {
		select {
		case <-time.After(tr.trace.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	response, err := tr.base.RoundTrip(req)
	if err == nil && drop {
		response.Body.Close()
		return nil, errors.New("lost response after fixture admission")
	}
	return response, err
}

// This same driver can be run against the former auto-ready lifecycle. Stage
// regressions in provenance_flow_test.go independently enforce the new contract.
func TestStageProvenanceHTTP(t *testing.T) {
	ctx := context.Background()
	trace := new(sourceTrace)
	if raw := os.Getenv("STAGE_BOUNDARY_DELAY_MS"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 0 || ms > 1000 {
			t.Fatal("invalid STAGE_BOUNDARY_DELAY_MS")
		}
		trace.delay = time.Duration(ms) * time.Millisecond
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = auditTLSConfig() // Loopback test certificate.
	t.Cleanup(transport.CloseIdleConnections)
	client := func(service string) *http.Client {
		return &http.Client{Transport: sourceStageTransport{service, transport, trace}}
	}
	var rows []sourceStageSample
	var main *testProver
	var source *sourceFlowProver
	mainCount := func() int {
		if main == nil {
			return 0
		}
		return len(main.requests)
	}
	sourceCount := func() int64 {
		if source == nil {
			return 0
		}
		return source.calls.Load()
	}
	stage := func(name string, work func() error) {
		t.Helper()
		initialMain, initialSource := mainCount(), sourceCount()
		start := time.Now()
		err := work()
		elapsed := time.Since(start)
		trace.mu.Lock()
		requests := append([]sourceStageRequest(nil), trace.requests...)
		trace.requests = nil
		trace.mu.Unlock()
		rows = append(rows, sourceStageSample{name, requests, mainCount() - initialMain, sourceCount() - initialSource, float64(elapsed) / float64(time.Millisecond)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var s *WalletSession
	var token common.Address
	stage("initialize", func() error {
		s, token, main = testSessionWithClients(t, client("protocol"), func(fixture sessionProvider) sdk.Provider {
			url := auditRPCEndpoint(t, &submissionProvider{sessionProvider: fixture}, &submitHTTPAudit{methods: map[string]int{}})
			rpc, err := gethrpc.DialOptions(ctx, url, gethrpc.WithHTTPClient(client("chain")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(rpc.Close)
			return ethclient.NewClient(rpc)
		}, uint64(time.Now().Unix()))
		return nil
	})
	var op *Operation
	var c *broadcaster.Client
	var err error
	stage("prepare", func() error {
		address, e := s.wallet.GetAddress()
		if e != nil {
			return e
		}
		op, err = s.PrepareTransfer(ctx, sdk.TransferPreparation{TokenAddress: token, Outputs: testOutputs(s, []testPaymentOutput{{To: address, Amount: big.NewInt(30)}})})
		return err
	})
	stage("fixtureSetup", func() error {
		source, _ = configureSourceFlow(t, s, op, false, false)
		s.options.Provenance.Client.HTTP = client("source")
		relay, _ := testBroadcasterExecution(t, op, []int64{5}, false, nil, "direct", nil, common.Hash{})
		c, err = broadcaster.NewClient(relay.Configuration(), broadcaster.Options{HTTPClient: client("broadcaster")})
		return err
	})
	stage("quote", func() error { return c.Quote(ctx, op, sdk.BroadcasterQuoteOptions{MaxFeeAmount: big.NewInt(10)}) })
	stage("authorize", func() error { op, err = authorizeForTest(op, ctx); return err })
	stage("mainProve", func() error { op, err = op.Prove(ctx); return err })
	stage("ready", func() error {
		if op.Status() == "proved" {
			op, err = op.Ready(ctx)
			return err
		}
		return nil
	})
	trace.dropSubmit = true
	stage("submit", func() error {
		_, e := c.Submit(ctx, op)
		if e == nil || op.Status() != "uncertain" {
			return errors.New("expected response loss and frozen attempt")
		}
		return nil
	})
	trace.dropSubmit = false
	stage("restore", func() error { op, err = s.Resume(ctx, op.ID()); return err })
	stage("replay", func() error { _, e := c.Submit(ctx, op); return e })
	stage("status", func() error { _, e := c.Submit(ctx, op); return e })
	var packets []json.RawMessage
	for _, row := range rows {
		for _, r := range row.Requests {
			if r.Method == "broadcaster_submitTransaction" {
				packets = append(packets, r.Params)
			}
		}
	}
	if len(packets) != 2 || !bytes.Equal(packets[0], packets[1]) || mainCount() != 1 || sourceCount() != 1 {
		t.Fatal("recovery changed packet or reproved")
	}
	if dest := os.Getenv("STAGE_BOUNDARY_PROFILE_OUTPUT"); dest != "" {
		data, e := json.MarshalIndent(rows, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
