package gethexec

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/solgen/go/express_lane_auctiongen"
	"github.com/offchainlabs/nitro/timeboost"
	"github.com/offchainlabs/nitro/util/stopwaiter"
	"github.com/pkg/errors"
)

var auctionResolvedEvent common.Hash

func init() {
	auctionAbi, err := express_lane_auctiongen.ExpressLaneAuctionMetaData.GetAbi()
	if err != nil {
		panic(err)
	}
	auctionResolvedEventData, ok := auctionAbi.Events["AuctionResolved"]
	if !ok {
		panic("RollupCore ABI missing AssertionCreated event")
	}
	auctionResolvedEvent = auctionResolvedEventData.ID
}

type expressLaneControl struct {
	round      uint64
	sequence   uint64
	controller common.Address
}

type expressLaneService struct {
	stopwaiter.StopWaiter
	sync.RWMutex
	control             expressLaneControl
	auctionContractAddr common.Address
	auctionContract     *express_lane_auctiongen.ExpressLaneAuction
	initialTimestamp    time.Time
	roundDuration       time.Duration
	chainConfig         *params.ChainConfig
	logs                chan []*types.Log
	bc                  *core.BlockChain
}

func newExpressLaneService(
	auctionContractAddr common.Address,
	initialRoundTimestamp uint64,
	roundDuration time.Duration,
	bc *core.BlockChain,
) (*expressLaneService, error) {
	chainConfig := bc.Config()
	return &expressLaneService{
		bc:               bc,
		chainConfig:      chainConfig,
		initialTimestamp: time.Unix(int64(initialRoundTimestamp), 0),
		control: expressLaneControl{
			controller: common.Address{},
			round:      0,
		},
		auctionContractAddr: auctionContractAddr,
		roundDuration:       roundDuration,
		logs:                make(chan []*types.Log, 10_000),
	}, nil
}

func (es *expressLaneService) Start(ctxIn context.Context) {
	es.StopWaiter.Start(ctxIn, es)

	// Log every new express lane auction round.
	es.LaunchThread(func(ctx context.Context) {
		log.Info("Watching for new express lane rounds")
		now := time.Now()
		waitTime := es.roundDuration - time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond())
		time.Sleep(waitTime)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				round := timeboost.CurrentRound(es.initialTimestamp, es.roundDuration)
				log.Info(
					"New express lane auction round",
					"round", round,
					"timestamp", t,
				)
			}
		}
	})
	es.LaunchThread(func(ctx context.Context) {
		log.Info("Monitoring express lane auction contract")
		sub := es.bc.SubscribeLogsEvent(es.logs)
		defer sub.Unsubscribe()
		for {
			select {
			case evs := <-es.logs:
				for _, ev := range evs {
					if ev.Address != es.auctionContractAddr {
						continue
					}
					go es.processAuctionContractEvent(ctx, ev)
				}
			case <-ctx.Done():
				return
			case err := <-sub.Err():
				log.Error("Subscriber failed", "err", err)
				return
			}
		}
	})
	es.LaunchThread(func(ctx context.Context) {
		// Monitor for auction cancelations.
		// TODO: Implement.
	})
}

func (es *expressLaneService) processAuctionContractEvent(_ context.Context, rawLog *types.Log) {
	if !slices.Contains(rawLog.Topics, auctionResolvedEvent) {
		return
	}
	ev, err := es.auctionContract.ParseAuctionResolved(*rawLog)
	if err != nil {
		log.Error("Failed to parse AuctionResolved event", "err", err)
		return
	}
	log.Info(
		"New express lane controller assigned",
		"round", ev.Round,
		"controller", ev.FirstPriceExpressLaneController,
	)
	es.Lock()
	es.control.round = ev.Round
	es.control.controller = ev.FirstPriceExpressLaneController
	es.control.sequence = 0 // Sequence resets 0 for the new round.
	es.Unlock()
}

func (es *expressLaneService) currentRoundHasController() bool {
	es.Lock()
	defer es.Unlock()
	return es.control.controller != (common.Address{})
}

func (es *expressLaneService) validateExpressLaneTx(msg *timeboost.ExpressLaneSubmission) error {
	if msg.Transaction == nil || msg.Signature == nil {
		return timeboost.ErrMalformedData
	}
	if msg.AuctionContractAddress != es.auctionContractAddr {
		return timeboost.ErrWrongAuctionContract
	}
	if !es.currentRoundHasController() {
		return timeboost.ErrNoOnchainController
	}
	if msg.ChainId.Cmp(es.chainConfig.ChainID) != 0 {
		return errors.Wrapf(timeboost.ErrWrongChainId, "express lane tx chain ID %d does not match current chain ID %d", msg.ChainId, es.chainConfig.ChainID)
	}
	currentRound := timeboost.CurrentRound(es.initialTimestamp, es.roundDuration)
	if msg.Round != currentRound {
		return errors.Wrapf(timeboost.ErrBadRoundNumber, "express lane tx round %d does not match current round %d", msg.Round, currentRound)
	}
	// Reconstruct the message being signed over and recover the sender address.
	signingMessage, err := msg.ToMessageBytes()
	if err != nil {
		return timeboost.ErrMalformedData
	}
	if len(msg.Signature) != 65 {
		return errors.Wrap(timeboost.ErrMalformedData, "signature length is not 65")
	}
	// Recover the public key.
	prefixed := crypto.Keccak256(append([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(signingMessage))), signingMessage...))
	sigItem := make([]byte, len(msg.Signature))
	copy(sigItem, msg.Signature)
	if sigItem[len(sigItem)-1] >= 27 {
		sigItem[len(sigItem)-1] -= 27
	}
	pubkey, err := crypto.SigToPub(prefixed, sigItem)
	if err != nil {
		return timeboost.ErrMalformedData
	}
	if !secp256k1.VerifySignature(crypto.FromECDSAPub(pubkey), prefixed, sigItem[:len(sigItem)-1]) {
		return timeboost.ErrWrongSignature
	}
	sender := crypto.PubkeyToAddress(*pubkey)
	es.Lock()
	defer es.Unlock()
	if sender != es.control.controller {
		return timeboost.ErrNotExpressLaneController
	}
	return nil
}
