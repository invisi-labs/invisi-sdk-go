package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

func (c *ProtocolRpcClient) GetChains(ctx context.Context) ([]Chain, error) {
	target := c.url
	if !strings.HasSuffix(strings.TrimSuffix(target.Path, "/"), "/v1") {
		return nil, errors.New("chain discovery requires service /v1 URL")
	}
	var response struct {
		APIVersion string  `json:"apiVersion"`
		Chains     []Chain `json:"chains"`
	}
	if e := c.request(ctx, target, "GET", nil, &response); e != nil {
		return nil, e
	}
	if response.APIVersion != "v1" || response.Chains == nil {
		return nil, errors.New("unsupported service response")
	}
	seen := map[string]bool{}
	for i := range response.Chains {
		ch := &response.Chains[i]
		n, e := strconv.ParseUint(ch.ChainID, 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != ch.ChainID || seen[ch.ChainID] || ch.ProtocolVersion != "1" || ch.RPCPath != "/v1/chains/"+ch.ChainID {
			return nil, errors.New("invalid chain discovery entry")
		}
		seen[ch.ChainID] = true
		ch.RPCURL = target.String()
		u := target
		u.Path = strings.TrimSuffix(u.Path, "/") + "/chains/" + ch.ChainID
		ch.RPCURL = u.String()
	}
	return response.Chains, nil
}

func (c *ProtocolRpcClient) GetDeployment(ctx context.Context, chain uint64) (ProtocolDeployment, error) {
	c.deploymentRequests.Add(1)
	return c.getDeployment(ctx, chain)
}

// DeploymentObservation identifies a successful service lookup and its HTTP
// policy. A new lookup on the original client or a provider clone retires it.
type DeploymentObservation struct {
	requests    *atomic.Uint64
	request     uint64
	ownedPolicy bool
}

// Current reports whether no newer deployment lookup has started.
func (o *DeploymentObservation) Current() bool {
	return o != nil && o.requests != nil && o.requests.Load() == o.request
}

// Reusable reports whether a later initialization may use this observation.
func (o *DeploymentObservation) Reusable() bool { return o.Current() && o.ownedPolicy }

// VerifyDeployment contacts the selected service and checks the complete
// deployment. A matching response still succeeds when requests overlap. Callers
// publishing a capability must check Current because a newer lookup may have
// retired this observation, including before this method returns.
func (c *ProtocolRpcClient) VerifyDeployment(ctx context.Context, expected ProtocolDeployment) (*DeploymentObservation, error) {
	return c.verifyDeployment(ctx, expected, c.deploymentRequests.Add(1))
}

// BeginDeploymentVerification reserves the request generation immediately. The
// returned function performs the lookup once, allowing callers to order the
// start of a request with local state changes without holding a lock during I/O.
func (c *ProtocolRpcClient) BeginDeploymentVerification(ctx context.Context, expected ProtocolDeployment) func() (*DeploymentObservation, error) {
	request := c.deploymentRequests.Add(1)
	expected = cloneDeployment(expected)
	return sync.OnceValues(func() (*DeploymentObservation, error) {
		return c.verifyDeployment(ctx, expected, request)
	})
}

func (c *ProtocolRpcClient) verifyDeployment(ctx context.Context, expected ProtocolDeployment, request uint64) (*DeploymentObservation, error) {
	chain, err := expected.ChainNumber()
	if err != nil {
		return nil, err
	}
	d, err := c.getDeployment(ctx, chain.Uint64())
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(d, expected) {
		return nil, errors.New("Protocol RPC deployment mismatch")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &DeploymentObservation{requests: c.deploymentRequests, request: request, ownedPolicy: c.http.Transport == nativeServiceTransport}, nil
}

func (c *ProtocolRpcClient) getDeployment(ctx context.Context, chain uint64) (ProtocolDeployment, error) {
	var response struct {
		APIVersion      string             `json:"apiVersion"`
		ProtocolVersion string             `json:"protocolVersion"`
		Deployment      ProtocolDeployment `json:"deployment"`
	}
	if e := c.call(ctx, chain, "invisi_getDeployment", []any{}, &response); e != nil {
		return ProtocolDeployment{}, e
	}
	d := response.Deployment
	if e := d.Validate(); e != nil {
		return d, e
	}
	if response.APIVersion != "v1" || response.ProtocolVersion != "1" || d.ChainID != strconv.FormatUint(chain, 10) {
		return d, errors.New("deployment version or chain mismatch")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deployment != nil {
		a, _ := json.Marshal(c.deployment)
		b, _ := json.Marshal(d)
		if !bytes.Equal(a, b) {
			return d, errors.New("deployment pins mismatch")
		}
	}
	copy := cloneDeployment(d)
	c.deployment = &copy
	return cloneDeployment(d), nil
}
