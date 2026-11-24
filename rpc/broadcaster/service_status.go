package broadcaster

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

type SchedulingCapabilities struct {
	Enabled                       bool   `json:"enabled"`
	MaxDelaySeconds               string `json:"maxDelaySeconds"`
	DefaultExecutionWindowSeconds string `json:"defaultExecutionWindowSeconds"`
	MaxExecutionWindowSeconds     string `json:"maxExecutionWindowSeconds"`
	MinDeadlineSlackSeconds       string `json:"minDeadlineSlackSeconds"`
}

type FeeLockCapabilities struct {
	Version string `json:"version"`
	Model   string `json:"model"`
	Core    bool   `json:"core"`
	Router  bool   `json:"router"`
}
type ServiceStatus struct {
	FeeLock          *FeeLockCapabilities   `json:"feeLock,omitempty"`
	Scheduling       SchedulingCapabilities `json:"scheduling"`
	APIVersion       string                 `json:"apiVersion"`
	ProtocolVersion  string                 `json:"protocolVersion"`
	ChainID          string                 `json:"chainId"`
	Protocol         common.Address         `json:"protocol"`
	Sender           common.Address         `json:"sender"`
	FeeRecipient     common.Address         `json:"feeRecipient"`
	SubmissionHalted bool                   `json:"submissionHalted"`
	Provenance       struct {
		AggregateSupported bool   `json:"aggregateSupported"`
		Enabled            bool   `json:"enabled"`
		RequestHashFormat  string `json:"requestHashFormat"`
		TrackingRequired   bool   `json:"trackingRequired"`
		TrackingSupported  bool   `json:"trackingSupported"`
	} `json:"provenance"`
}

func (c *Client) SubmissionRequirements(ctx context.Context, s SignedQuote) (bool, error) {
	if s.Context == nil {
		return false, errors.New("missing verified quote context")
	}
	receipt, _ := c.preparationSnapshot()
	status, e := c.readServiceStatus(ctx, *s.Context)
	c.observePreparation(receipt, status, e)
	if e != nil {
		return false, e
	}
	if status.SubmissionHalted {
		return false, errors.New("broadcaster submission halted")
	}
	p := status.Provenance
	if s.Quote.Execution == "aggregate" && !p.AggregateSupported {
		return false, errors.New("broadcaster aggregate execution unavailable")
	}
	if p.Enabled != s.ProvenanceRequired || (p.Enabled && (p.RequestHashFormat != ProvenanceRequestHashFormat || !p.TrackingRequired || !p.TrackingSupported)) {
		return false, errors.New("broadcaster provenance requirement changed; obtain a new quote")
	}
	return p.Enabled, nil
}

func (c *Client) SupportsToken(ctx context.Context, token common.Address, trust Trust) (bool, error) {
	receipt, _ := c.preparationSnapshot()
	supported, status, err := c.tokenSupport(ctx, token, trust)
	c.observePreparation(receipt, status, err)
	return supported, err
}

func (c *Client) ServiceStatus(ctx context.Context, trust Trust) (ServiceStatus, error) {
	receipt, _ := c.preparationSnapshot()
	status, err := c.readServiceStatus(ctx, trust)
	c.observePreparation(receipt, status, err)
	return status, err
}

func (c *Client) checkedServiceStatus(status ServiceStatus, trust Trust) (ServiceStatus, error) {
	n, ok := new(big.Int).SetString(status.ChainID, 10)
	if !ok || trust.ChainID == nil || n.Cmp(trust.ChainID) != 0 || status.Protocol != trust.Protocol || status.Sender != c.configuration.QuoteSigner || status.FeeRecipient != c.configuration.FeeRecipient || status.APIVersion != "v1" || status.ProtocolVersion != "1" {
		return status, errors.New("invalid broadcaster service status")
	}
	if status.FeeLock != nil && (status.FeeLock.Version != "1" || status.FeeLock.Model != feeLockModel) {
		return status, errors.New("unsupported fee lock capability")
	}
	if status.Scheduling.Enabled {
		for _, v := range []string{status.Scheduling.MaxDelaySeconds, status.Scheduling.DefaultExecutionWindowSeconds, status.Scheduling.MaxExecutionWindowSeconds} {
			if _, e := settlement.ScheduleSeconds(v); e != nil {
				return status, e
			}
		}
		slack, e := decimal(status.Scheduling.MinDeadlineSlackSeconds)
		if e != nil || slack.BitLen() > 53 || slack.String() != status.Scheduling.MinDeadlineSlackSeconds {
			return status, errors.New("invalid scheduling deadline slack")
		}
	}
	return status, nil
}

// A present capability must not silently become an absent/disabled one.
func (c *FeeLockCapabilities) UnmarshalJSON(data []byte) error {
	var value struct {
		Version string `json:"version"`
		Model   string `json:"model"`
		Core    *bool  `json:"core"`
		Router  *bool  `json:"router"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.Core == nil || value.Router == nil {
		return errors.New("invalid fee lock capability")
	}
	*c = FeeLockCapabilities{Version: value.Version, Model: value.Model, Core: *value.Core, Router: *value.Router}
	return nil
}
