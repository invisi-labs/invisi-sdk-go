package transaction

import (
	"context"

	protocolrpc "github.com/invisi-labs/invisi-sdk-go/rpc"
)

func (s *Runtime) PersistPlan(ctx context.Context, plan Plan) (*Execution, error) {
	return s.persistPlan(ctx, plan, s.Canonical)
}

func (s *Runtime) persistPlan(ctx context.Context, plan Plan, checkCanonical func(context.Context, protocolrpc.CanonicalBlock) error) (*Execution, error) {
	if e := checkCanonical(ctx, plan.Block); e != nil {
		return nil, e
	}
	id, e := RandomOperationID()
	if e != nil {
		return nil, e
	}
	if s.Snapshot != nil {
		plan.SubmissionValidation = "required"
	}
	plan.ID = id
	plan.Version = 1
	if plan.RetainedShieldAnchor {
		plan.Version = 2
	}
	plan.OwnerAddress = s.Wallet.ownerAddress
	plan.Namespace = s.Scope.Namespace
	plan, e = ClonePlan(plan)
	if e != nil {
		return nil, e
	}
	execution := &Execution{Runtime: s, Plan: plan, Status: "prepared", WorkerID: id}
	if s.OnComplete != nil {
		if e = s.OnComplete(ctx, execution); e != nil {
			return nil, e
		}
	}
	return execution, nil
}
