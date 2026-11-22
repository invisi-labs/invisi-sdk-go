package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-sdk-go/primitives"
)

func TestResumeRejectsForeignWalletAndDeploymentWithoutChangingJournal(t *testing.T) {
	for _, mode := range []string{"owner", "chain", "protocol"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			prepared, grant := testPlan(t)
			store := NewMemoryStore()
			pinned := grant.Delivery.State.ProtocolContext
			owner := common.BigToAddress(primitives.ElementToBigInt(grant.Delivery.State.SpenderWalletIdentity))
			session, err := New(Options{Store: store, OwnerAddress: owner, Context: &pinned})
			if err != nil {
				t.Fatal(err)
			}
			if err = session.Track(ctx, grant); err != nil {
				t.Fatal(err)
			}
			original, err := session.Prepare(ctx, "draw", prepared)
			if err != nil {
				t.Fatal(err)
			}
			foreign := pinned
			options := Options{Store: store, OwnerAddress: owner, Context: &foreign}
			switch mode {
			case "owner":
				options.OwnerAddress = common.HexToAddress("0xbeef")
			case "chain":
				foreign.ChainID = primitives.ElementFromUint64(2)
			case "protocol":
				foreign.ProtocolAddress = primitives.ElementFromUint64(18)
			}
			selected, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = selected.Prepare(ctx, "foreign", prepared); err == nil {
				t.Fatal("foreign preparation accepted")
			}
			before, err := store.ReadSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = selected.Resume(ctx, "draw", nil); err == nil {
				t.Fatal("foreign operation restored")
			}
			after, err := store.ReadSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected recovery changed journal or reservations")
			}
			if err = original.Abandon(ctx); err != nil {
				t.Fatalf("rejected recovery invalidated original handle: %v", err)
			}
		})
	}
}

func TestResumeMatchingScopeRetainsNormalLeaseAndReservationSemantics(t *testing.T) {
	ctx := context.Background()
	prepared, grant := testPlan(t)
	pinned := grant.Delivery.State.ProtocolContext
	store := NewMemoryStore()
	owner := common.BigToAddress(primitives.ElementToBigInt(grant.Delivery.State.SpenderWalletIdentity))
	session, err := New(Options{Store: store, OwnerAddress: owner, Context: &pinned})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Track(ctx, grant); err != nil {
		t.Fatal(err)
	}
	original, err := session.Prepare(ctx, "draw", prepared)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := GrantKey(grant.Commitment, grant.Role)
	if reserved, exists := before.Grants[key]; !exists || reserved.ReservedBy != "draw" {
		t.Fatal("preparation did not reserve the input")
	}
	resumed, err := session.Resume(ctx, "draw", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = original.Abandon(ctx); err == nil {
		t.Fatal("old handle retained lease")
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Grants, state.Grants) {
		t.Fatal("resume changed input reservations")
	}
	if state.Operations["draw"].WorkerID == before.Operations["draw"].WorkerID {
		t.Fatal("resume did not replace the worker lease")
	}
	if err = resumed.Abandon(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if released, exists := state.Grants[key]; !exists || released.ReservedBy != "" || released.Consumed {
		t.Fatal("resumed handle did not release the unconsumed input")
	}
}
