package sync

import (
	"bytes"
	"context"
	"time"

	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"

	libp2pcore "github.com/libp2p/go-libp2p-core"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/pkg/errors"
	pb "github.com/prysmaticlabs/prysm/proto/beacon/p2p/v1"
	"github.com/prysmaticlabs/prysm/shared/params"
	"github.com/prysmaticlabs/prysm/shared/roughtime"
)

const statusInterval = 6 * time.Minute // 60 slots.

// maintainPeerStatuses by infrequently polling peers for their latest status.
func (r *RegularSync) maintainPeerStatuses() {
	ticker := time.NewTicker(statusInterval)
	for {
		ctx := context.Background()
		select {
		case <-ticker.C:
			for _, pid := range r.p2p.Peers().Connected() {
				// If the status hasn't been updated in the recent interval time.
				lastUpdated, err := r.p2p.Peers().ChainStateLastUpdated(pid)
				if err != nil {
					// Peer has vanished; nothing to do
					continue
				}
				if roughtime.Now().After(lastUpdated.Add(statusInterval)) {
					if err := r.sendRPCStatusRequest(ctx, pid); err != nil {
						log.WithError(err).Error("Failed to request peer status")
					}
				}
			}
			_, highestEpoch := r.p2p.Peers().HighestFinalizedPeer()
			if highestEpoch > r.chain.FinalizedCheckpt().Epoch {
				r.clearPendingSlots()
				// block until we can resync the node
				if err := r.initialSync.Resync(); err != nil {
					log.Errorf("Could not Resync Chain: %v", err)
				}
			}
		case <-r.ctx.Done():
			return
		}
	}
}

// sendRPCStatusRequest for a given topic with an expected protobuf message type.
func (r *RegularSync) sendRPCStatusRequest(ctx context.Context, id peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp := &pb.Status{
		HeadForkVersion: r.chain.CurrentFork().CurrentVersion,
		FinalizedRoot:   r.chain.FinalizedCheckpt().Root,
		FinalizedEpoch:  r.chain.FinalizedCheckpt().Epoch,
		HeadRoot:        r.chain.HeadRoot(),
		HeadSlot:        r.chain.HeadSlot(),
	}
	stream, err := r.p2p.Send(ctx, resp, id)
	if err != nil {
		return err
	}

	code, errMsg, err := ReadStatusCode(stream, r.p2p.Encoding())
	if err != nil {
		return err
	}

	if code != 0 {
		r.p2p.Peers().IncrementBadResponses(stream.Conn().RemotePeer())
		return errors.New(errMsg)
	}

	msg := &pb.Status{}
	if err := r.p2p.Encoding().DecodeWithLength(stream, msg); err != nil {
		return err
	}
	r.p2p.Peers().SetChainState(stream.Conn().RemotePeer(), msg)

	err = r.validateStatusMessage(msg, stream)
	if err != nil {
		r.p2p.Peers().IncrementBadResponses(stream.Conn().RemotePeer())
	}
	return err
}

func (r *RegularSync) removeDisconnectedPeerStatus(ctx context.Context, pid peer.ID) error {
	return nil
}

// statusRPCHandler reads the incoming Status RPC from the peer and responds with our version of a status message.
// This handler will disconnect any peer that does not match our fork version.
func (r *RegularSync) statusRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	defer stream.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	setRPCStreamDeadlines(stream)
	log := log.WithField("handler", "status")
	m := msg.(*pb.Status)

	if err := r.validateStatusMessage(m, stream); err != nil {
		log.WithField("peer", stream.Conn().RemotePeer()).Warn("Invalid fork version from peer")
		r.p2p.Peers().IncrementBadResponses(stream.Conn().RemotePeer())
		originalErr := err
		resp, err := r.generateErrorResponse(responseCodeInvalidRequest, err.Error())
		if err != nil {
			log.WithError(err).Error("Failed to generate a response error")
		} else {
			if _, err := stream.Write(resp); err != nil {
				log.WithError(err).Errorf("Failed to write to stream")
			}
		}
		stream.Close() // Close before disconnecting.
		// Add a short delay to allow the stream to flush before closing the connection.
		// There is still a chance that the peer won't receive the message.
		time.Sleep(50 * time.Millisecond)
		if err := r.p2p.Disconnect(stream.Conn().RemotePeer()); err != nil {
			log.WithError(err).Error("Failed to disconnect from peer")
		}
		return originalErr
	}
	r.p2p.Peers().SetChainState(stream.Conn().RemotePeer(), m)

	resp := &pb.Status{
		HeadForkVersion: r.chain.CurrentFork().CurrentVersion,
		FinalizedRoot:   r.chain.FinalizedCheckpt().Root,
		FinalizedEpoch:  r.chain.FinalizedCheckpt().Epoch,
		HeadRoot:        r.chain.HeadRoot(),
		HeadSlot:        r.chain.HeadSlot(),
	}

	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		log.WithError(err).Error("Failed to write to stream")
	}
	_, err := r.p2p.Encoding().EncodeWithLength(stream, resp)

	return err
}

func (r *RegularSync) validateStatusMessage(msg *pb.Status, stream network.Stream) error {
	if !bytes.Equal(params.BeaconConfig().GenesisForkVersion, msg.HeadForkVersion) {
		return errWrongForkVersion
	}
	genesis := r.chain.GenesisTime()
	slotsSinceGenesis := uint64(roughtime.Since(genesis).Seconds()) / params.BeaconConfig().SecondsPerSlot
	expectedEpoch := helpers.SlotToEpoch(slotsSinceGenesis)
	if msg.FinalizedEpoch > expectedEpoch {
		return errInvalidEpoch
	}
	return nil
}
