package sync

import (
	"bytes"
	"context"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	"github.com/prysmaticlabs/go-ssz"
	mock "github.com/prysmaticlabs/prysm/beacon-chain/blockchain/testing"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p"
	p2ptest "github.com/prysmaticlabs/prysm/beacon-chain/p2p/testing"
	mockSync "github.com/prysmaticlabs/prysm/beacon-chain/sync/initial-sync/testing"
	pb "github.com/prysmaticlabs/prysm/proto/beacon/p2p/v1"
	"github.com/prysmaticlabs/prysm/shared/bls"
	"github.com/prysmaticlabs/prysm/shared/params"
	"github.com/prysmaticlabs/prysm/shared/testutil"
)

func setupValidAttesterSlashing(t *testing.T) (*ethpb.AttesterSlashing, *pb.BeaconState) {
	state, privKeys := testutil.DeterministicGenesisState(t, 5)
	for _, vv := range state.Validators {
		vv.WithdrawableEpoch = 1 * params.BeaconConfig().SlotsPerEpoch
	}

	att1 := &ethpb.IndexedAttestation{
		Data: &ethpb.AttestationData{
			Source: &ethpb.Checkpoint{Epoch: 1},
			Target: &ethpb.Checkpoint{Epoch: 0},
		},
		CustodyBit_0Indices: []uint64{0, 1},
	}
	dataAndCustodyBit := &pb.AttestationDataAndCustodyBit{
		Data:       att1.Data,
		CustodyBit: false,
	}
	hashTreeRoot, err := ssz.HashTreeRoot(dataAndCustodyBit)
	if err != nil {
		t.Error(err)
	}
	domain := helpers.Domain(state.Fork, 0, params.BeaconConfig().DomainBeaconAttester)
	sig0 := privKeys[0].Sign(hashTreeRoot[:], domain)
	sig1 := privKeys[1].Sign(hashTreeRoot[:], domain)
	aggregateSig := bls.AggregateSignatures([]*bls.Signature{sig0, sig1})
	att1.Signature = aggregateSig.Marshal()[:]

	att2 := &ethpb.IndexedAttestation{
		Data: &ethpb.AttestationData{
			Source: &ethpb.Checkpoint{Epoch: 0},
			Target: &ethpb.Checkpoint{Epoch: 0},
		},
		CustodyBit_0Indices: []uint64{0, 1},
	}
	dataAndCustodyBit = &pb.AttestationDataAndCustodyBit{
		Data:       att2.Data,
		CustodyBit: false,
	}
	hashTreeRoot, err = ssz.HashTreeRoot(dataAndCustodyBit)
	if err != nil {
		t.Error(err)
	}
	sig0 = privKeys[0].Sign(hashTreeRoot[:], domain)
	sig1 = privKeys[1].Sign(hashTreeRoot[:], domain)
	aggregateSig = bls.AggregateSignatures([]*bls.Signature{sig0, sig1})
	att2.Signature = aggregateSig.Marshal()[:]

	slashing := &ethpb.AttesterSlashing{
		Attestation_1: att1,
		Attestation_2: att2,
	}

	currentSlot := 2 * params.BeaconConfig().SlotsPerEpoch
	state.Slot = currentSlot

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return slashing, state
}

func TestValidateAttesterSlashing_ValidSlashing(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidAttesterSlashing(t)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: s},
		initialSync: &mockSync.Sync{IsSyncing: false},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}

	msg := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateAttesterSlashing(ctx, peer.ID("foobar"), msg)

	if !valid {
		t.Error("Failed Validation")
	}

	if msg.ValidatorData == nil {
		t.Error("Decoded message was not set on the message validator data")
	}
}

func TestValidateAttesterSlashing_ContextTimeout(t *testing.T) {
	p := p2ptest.NewTestP2P(t)

	slashing, state := setupValidAttesterSlashing(t)
	slashing.Attestation_1.Data.Target.Epoch = 100000000

	ctx, _ := context.WithTimeout(context.Background(), 100*time.Millisecond)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: state},
		initialSync: &mockSync.Sync{IsSyncing: false},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}

	msg := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateAttesterSlashing(ctx, "", msg)

	if valid {
		t.Error("slashing from the far distant future should have timed out and returned false")
	}
}

func TestValidateAttesterSlashing_Syncing(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidAttesterSlashing(t)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: s},
		initialSync: &mockSync.Sync{IsSyncing: true},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}
	msg := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateAttesterSlashing(ctx, "", msg)
	if valid {
		t.Error("Passed validation")
	}
}
