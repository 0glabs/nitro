package gethexec

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/solgen/go/express_lane_auctiongen"
	"github.com/offchainlabs/nitro/timeboost"
	"github.com/offchainlabs/nitro/util/stopwaiter"
	"github.com/pkg/errors"
)

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
	initialTimestamp    time.Time
	roundDuration       time.Duration
	chainConfig         *params.ChainConfig
	logs                chan []*types.Log
	seqClient           *ethclient.Client
	auctionContract     *express_lane_auctiongen.ExpressLaneAuction
}

func newExpressLaneService(
	auctionContractAddr common.Address,
	sequencerClient *ethclient.Client,
	bc *core.BlockChain,
) (*expressLaneService, error) {
	chainConfig := bc.Config()
	auctionContract, err := express_lane_auctiongen.NewExpressLaneAuction(auctionContractAddr, sequencerClient)
	if err != nil {
		return nil, err
	}
	roundTimingInfo, err := auctionContract.RoundTimingInfo(&bind.CallOpts{})
	if err != nil {
		return nil, err
	}
	initialTimestamp := time.Unix(int64(roundTimingInfo.OffsetTimestamp), 0)
	roundDuration := time.Duration(roundTimingInfo.RoundDurationSeconds) * time.Second
	return &expressLaneService{
		auctionContract:  auctionContract,
		chainConfig:      chainConfig,
		initialTimestamp: initialTimestamp,
		control: expressLaneControl{
			controller: common.Address{},
			round:      0,
		},
		auctionContractAddr: auctionContractAddr,
		roundDuration:       roundDuration,
		seqClient:           sequencerClient,
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
		// Monitor for auction resolutions from the auction manager smart contract
		// and set the express lane controller for the upcoming round accordingly.
		latestBlock, err := es.seqClient.HeaderByNumber(ctx, nil)
		if err != nil {
			log.Crit("Could not get latest header", "err", err)
		}
		fromBlock := latestBlock.Number.Uint64()
		ticker := time.NewTicker(time.Millisecond * 250)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				latestBlock, err := es.seqClient.HeaderByNumber(ctx, nil)
				if err != nil {
					log.Error("Could not get latest header", "err", err)
					continue
				}
				toBlock := latestBlock.Number.Uint64()
				if fromBlock == toBlock {
					continue
				}
				filterOpts := &bind.FilterOpts{
					Context: ctx,
					Start:   fromBlock,
					End:     &toBlock,
				}
				it, err := es.auctionContract.FilterAuctionResolved(filterOpts, nil, nil, nil)
				if err != nil {
					log.Error("Could not filter auction resolutions", "error", err)
					continue
				}
				for it.Next() {
					log.Info(
						"New express lane controller assigned",
						"round", it.Event.Round,
						"controller", it.Event.FirstPriceExpressLaneController,
					)
					es.Lock()
					es.control.round = it.Event.Round
					es.control.controller = it.Event.FirstPriceExpressLaneController
					es.control.sequence = 0 // Sequence resets 0 for the new round.
					es.Unlock()
				}
				fromBlock = toBlock
			}
		}
	})
	es.LaunchThread(func(ctx context.Context) {
		// Monitor for auction cancelations.
		// TODO: Implement.
	})
}

func (es *expressLaneService) currentRoundHasController() bool {
	es.Lock()
	defer es.Unlock()
	return es.control.controller != (common.Address{})
}

// TODO: Validate sequence numbers are correct.
func (es *expressLaneService) validateExpressLaneTx(msg *timeboost.ExpressLaneSubmission) error {
	if msg.Transaction == nil || msg.Signature == nil {
		return timeboost.ErrMalformedData
	}
	if msg.ChainId.Cmp(es.chainConfig.ChainID) != 0 {
		return errors.Wrapf(timeboost.ErrWrongChainId, "express lane tx chain ID %d does not match current chain ID %d", msg.ChainId, es.chainConfig.ChainID)
	}
	if msg.AuctionContractAddress != es.auctionContractAddr {
		return timeboost.ErrWrongAuctionContract
	}
	if !es.currentRoundHasController() {
		return timeboost.ErrNoOnchainController
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
