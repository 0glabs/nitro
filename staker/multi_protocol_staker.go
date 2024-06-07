package staker

import (
	"context"

	"github.com/OffchainLabs/bold/solgen/go/bridgegen"
	boldrollup "github.com/OffchainLabs/bold/solgen/go/rollupgen"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/offchainlabs/nitro/util/headerreader"
	"github.com/offchainlabs/nitro/util/stopwaiter"
)

var assertionCreatedId common.Hash

func init() {
	rollupAbi, err := boldrollup.RollupCoreMetaData.GetAbi()
	if err != nil {
		panic(err)
	}
	assertionCreatedEvent, ok := rollupAbi.Events["AssertionCreated"]
	if !ok {
		panic("RollupCore ABI missing AssertionCreated event")
	}
	assertionCreatedId = assertionCreatedEvent.ID
}

type MultiProtocolStaker struct {
	stopwaiter.StopWaiter
	bridge *bridgegen.IBridge
}

func NewMultiProtocolStaker(
	l1Reader *headerreader.HeaderReader,
	wallet ValidatorWalletInterface,
	callOpts bind.CallOpts,
	config L1ValidatorConfig,
	blockValidator *BlockValidator,
	statelessBlockValidator *StatelessBlockValidator,
	stakedNotifiers []LatestStakedNotifier,
	confirmedNotifiers []LatestConfirmedNotifier,
	validatorUtilsAddress common.Address,
	bridgeAddress common.Address,
	fatalErr chan<- error,
) (*MultiProtocolStaker, error) {
	return nil, nil
}

func (m *MultiProtocolStaker) IsWhitelisted(ctx context.Context) (bool, error) {
	return false, nil
}

func (m *MultiProtocolStaker) Initialize(ctx context.Context) error {
	return nil
}

func (m *MultiProtocolStaker) Start(ctxIn context.Context) {
	// s.StopWaiter.Start(ctxIn, s)
	// s.LaunchThread(s.broadcastLoop)
}

// 	switchedToBoldProtocol, err := s.checkAndSwitchToBoldStaker(ctxIn)
// 	if err != nil {
// 		log.Error("staker: error in checking switch to bold staker", "err", err)
// 		// TODO: Determine a better path of action here.
// 		return
// 	}
// 	if switchedToBoldProtocol {
// 		s.StopAndWait()
// 	}

// func (c *ChallengeProtocolSwitcher) shouldUseBoldStaker(ctx context.Context) (bool, common.Address, error) {
// 	var addr common.Address
// 	if !c.config.Bold.Enable {
// 		return false, addr, nil
// 	}
// 	callOpts := c.getCallOpts(ctx)
// 	rollupAddress, err := c.bridge.Rollup(callOpts)
// 	if err != nil {
// 		return false, addr, err
// 	}
// 	userLogic, err := rollupgen.NewRollupUserLogic(rollupAddress, s.client)
// 	if err != nil {
// 		return false, addr, err
// 	}
// 	_, err = userLogic.ExtraChallengeTimeBlocks(callOpts)
// 	// ExtraChallengeTimeBlocks does not exist in the the bold protocol.
// 	return err != nil, rollupAddress, nil
// }

// func (c *L1ValidatorConfig) ValidatorRequired() bool {
// 	if !c.Enable {
// 		return false
// 	}
// 	if c.Dangerous.WithoutBlockValidator {
// 		return false
// 	}
// 	if c.strategy == WatchtowerStrategy {
// 		return false
// 	}
// 	return true
// }

// func (v *L1Validator) shouldUseBoldStaker(ctx context.Context) (bool, error) {
// 	callOpts := v.getCallOpts(ctx)
// 	userLogic, err := rollupgen.NewRollupUserLogic(v.rollupAddress, v.client)
// 	if err != nil {
// 		return false, err
// 	}
// 	_, err = userLogic.ExtraChallengeTimeBlocks(callOpts)
// 	// ExtraChallengeTimeBlocks does not exist in the the bold protocol.
// 	return err != nil, nil
// }

// func (v *L1Validator) updateBoldBlockValidatorModuleRoot(ctx context.Context) error {
// 	if v.blockValidator == nil {
// 		return nil
// 	}
// 	boldRollup, err := boldrollup.NewRollupUserLogic(v.rollupAddress, v.client)
// 	if err != nil {
// 		return err
// 	}
// 	moduleRoot, err := boldRollup.WasmModuleRoot(v.getCallOpts(ctx))
// 	if err != nil {
// 		return err
// 	}
// 	if moduleRoot != v.lastWasmModuleRoot {
// 		err := v.blockValidator.SetCurrentWasmModuleRoot(moduleRoot)
// 		if err != nil {
// 			return err
// 		}
// 		v.lastWasmModuleRoot = moduleRoot
// 	} else if (moduleRoot == common.Hash{}) {
// 		return errors.New("wasmModuleRoot in rollup is zero")
// 	}
// 	return nil
// }

// func (s *Staker) getStakedInfo(ctx context.Context, walletAddr common.Address) (validator.GoGlobalState, error) {
// 	var zeroVal validator.GoGlobalState
// 	if s.config.Bold.Enable {
// 		rollupUserLogic, err := boldrollup.NewRollupUserLogic(s.rollupAddress, s.client)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		latestStaked, err := rollupUserLogic.LatestStakedAssertion(s.getCallOpts(ctx), walletAddr)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		if latestStaked == [32]byte{} {
// 			latestConfirmed, err := rollupUserLogic.LatestConfirmed(&bind.CallOpts{Context: ctx})
// 			if err != nil {
// 				return zeroVal, err
// 			}
// 			latestStaked = latestConfirmed
// 		}
// 		assertion, err := readBoldAssertionCreationInfo(ctx, rollupUserLogic, latestStaked)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		afterState := protocol.GoGlobalStateFromSolidity(assertion.AfterState.GlobalState)
// 		return validator.GoGlobalState{
// 			BlockHash:  afterState.BlockHash,
// 			SendRoot:   afterState.SendRoot,
// 			Batch:      afterState.Batch,
// 			PosInBatch: afterState.PosInBatch,
// 		}, nil
// 	}

// func (s *Staker) checkAndSwitchToBoldStaker(ctx context.Context) (bool, error) {
// 	shouldSwitch, rollupAddress, err := s.shouldUseBoldStaker(ctx)
// 	if err != nil {
// 		return false, err
// 	}
// 	if !shouldSwitch {
// 		return false, nil
// 	}
// 	auth, err := s.builder.Auth(ctx)
// 	if err != nil {
// 		return false, err
// 	}
// 	boldManager, err := NewBOLDChallengeManager(ctx, rollupAddress, auth, s.client, s.statelessBlockValidator, &s.config.Bold, s.wallet.DataPoster())
// 	if err != nil {
// 		return false, err
// 	}
// 	boldManager.Start(ctx)
// 	return true, nil
// }

// func (s *Staker) getStakedInfo(ctx context.Context, walletAddr common.Address) (validator.GoGlobalState, error) {
// 	var zeroVal validator.GoGlobalState
// 	if s.config.Bold.Enable {
// 		rollupUserLogic, err := boldrollup.NewRollupUserLogic(s.rollupAddress, s.client)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		latestStaked, err := rollupUserLogic.LatestStakedAssertion(s.getCallOpts(ctx), walletAddr)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		if latestStaked == [32]byte{} {
// 			latestConfirmed, err := rollupUserLogic.LatestConfirmed(&bind.CallOpts{Context: ctx})
// 			if err != nil {
// 				return zeroVal, err
// 			}
// 			latestStaked = latestConfirmed
// 		}
// 		assertion, err := readBoldAssertionCreationInfo(ctx, rollupUserLogic, latestStaked)
// 		if err != nil {
// 			return zeroVal, err
// 		}
// 		afterState := protocol.GoGlobalStateFromSolidity(assertion.AfterState.GlobalState)
// 		return validator.GoGlobalState{
// 			BlockHash:  afterState.BlockHash,
// 			SendRoot:   afterState.SendRoot,
// 			Batch:      afterState.Batch,
// 			PosInBatch: afterState.PosInBatch,
// 		}, nil
// 	}
// 	latestStaked, _, err := s.validatorUtils.LatestStaked(&s.baseCallOpts, s.rollupAddress, walletAddr)
// 	if err != nil {
// 		return zeroVal, err
// 	}
// 	stakerLatestStakedNodeGauge.Update(int64(latestStaked))
// 	if latestStaked == 0 {
// 		return zeroVal, nil
// 	}
// 	stakedInfo, err := s.rollup.LookupNode(ctx, latestStaked)
// 	if err != nil {
// 		return zeroVal, err
// 	}
// 	return stakedInfo.AfterState().GlobalState, nil
// }
