package validator

import (
	"bytes"
	"context"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/OffchainLabs/challenge-protocol-v2/execution"
	"github.com/OffchainLabs/challenge-protocol-v2/protocol"
	statemanager "github.com/OffchainLabs/challenge-protocol-v2/state-manager"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/backends"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// TODO: These are brittle and could break if the event sigs change in Solidity.
	vertexAddedEventSig = hexutil.MustDecode("0x4383ba11a7cd16be5880c5f674b93be38b3b1fcafd7a7b06151998fa2a675349")
	mergeEventSig       = hexutil.MustDecode("0x72b50597145599e4288d411331c925b40b33b0fa3cccadc1f57d2a1ab973553a")
	bisectEventSig      = hexutil.MustDecode("0x69d5465c81edf7aaaf2e5c6c8829500df87d84c87f8d5b1221b59eaeaca70d27")
)

func TestChallengeProtocol_AliceAndBob(t *testing.T) {
	// Tests that validators are able to reach a one step fork correctly
	// by playing the challenge game on their own upon observing leaves
	// they disagree with. Here's the example with Alice and Bob, in which
	// they narrow down their disagreement to a single WAVM opcode
	// in a small step subchallenge. In this first test, Alice will be the honest
	// validator and will be able to resolve a challenge via a one-step-proof.
	//
	// At the assertion chain level, the fork is at height 2.
	//
	//                [3]-[4]-[6]-alice
	//               /
	// [genesis]-[2]-
	//               \
	//                [3]-[4]-[6]-bob
	//
	// At the big step challenge level, the fork is at height 2.
	//
	//                    [3]-[4]-[6]-alice
	//                   /
	// [bigstep_root]-[2]
	//                   \
	//                    [3]-[4]-[6]-bob
	//
	//
	// At the small step challenge level the fork is at 2^10 opcodes.
	//
	//                          [2^10 + 1]----....many bisections...----[2^20]-alice
	//                         /
	// [small_step_root]-[2^10]
	//                         \
	//                          [2^10 + 1]-----....many bisections...----[2^20]-bob
	//
	//
	t.Run("two forked assertions at the same height", func(t *testing.T) {
		cfg := &challengeProtocolTestConfig{
			currentChainHeight: 6,
			// The latest assertion height each validator has seen.
			aliceHeight: 6,
			bobHeight:   6,
			// The heights at which the validators diverge in histories. In this test,
			// alice and bob start diverging at height 3.
			assertionDivergenceHeight:    3,
			numBigStepsAtAssertionHeight: 6,
			bigStepDivergenceHeight:      3,
			numSmallStepsAtBigStep:       execution.BigStepSize,
			smallStepDivergenceHeight:    1 << 10,
		}
		// Alice adds a challenge leaf 6, is presumptive.
		// Bob adds leaf 6.
		// Bob bisects to 4, is presumptive.
		// Alice bisects to 4.
		// Alice bisects to 2, is presumptive.
		// Bob merges to 2.
		// Bob bisects from 4 to 3, is presumptive.
		// Alice bisects from 4 to 3.
		// Both challengers are now at a one-step fork, we now await subchallenge resolution.
		cfg.expectedVerticesAdded = 2
		cfg.expectedBisections = 5
		cfg.expectedMerges = 1
		hook := test.NewGlobal()
		runChallengeIntegrationTest(t, hook, cfg)
		AssertLogsContain(t, hook, "Reached one-step-fork at 32")
		AssertLogsContain(t, hook, "Reached one-step-fork at 32")
	})
	t.Run("two validators opening leaves at same height, fork point is a power of two", func(t *testing.T) {
		t.Skip("Flakey")
		cfg := &challengeProtocolTestConfig{
			currentChainHeight:        8,
			aliceHeight:               8,
			bobHeight:                 8,
			assertionDivergenceHeight: 5,
		}
		cfg.expectedVerticesAdded = 2
		cfg.expectedBisections = 5
		cfg.expectedMerges = 1
		hook := test.NewGlobal()
		runChallengeIntegrationTest(t, hook, cfg)
		AssertLogsContain(t, hook, "Reached one-step-fork at 4")
		AssertLogsContain(t, hook, "Reached one-step-fork at 4")
	})
	t.Run("two validators opening leaves at heights 6 and 256", func(t *testing.T) {
		t.Skip("Flakey")
		cfg := &challengeProtocolTestConfig{
			currentChainHeight:        256,
			aliceHeight:               6,
			bobHeight:                 256,
			assertionDivergenceHeight: 4,
		}
		// With Alice starting at 256 and bisecting all the way down to 4
		// will take 6 bisections. Then, Alice bisects from 4 to 3. Bob bisects twice to 4 and 2.
		// We should see a total of 9 bisections and 2 merges.
		cfg.expectedVerticesAdded = 2
		cfg.expectedBisections = 9
		cfg.expectedMerges = 2
		hook := test.NewGlobal()
		runChallengeIntegrationTest(t, hook, cfg)
		AssertLogsContain(t, hook, "Reached one-step-fork at 3")
		AssertLogsContain(t, hook, "Reached one-step-fork at 3")
	})
	t.Run("two validators opening leaves at heights 129 and 256", func(t *testing.T) {
		t.Skip("Flakey")
		cfg := &challengeProtocolTestConfig{
			currentChainHeight:        256,
			aliceHeight:               129,
			bobHeight:                 256,
			assertionDivergenceHeight: 4,
		}
		// Same as the test case above but bob has 4 more bisections to perform
		// if Bob starts at 129.
		cfg.expectedVerticesAdded = 2
		cfg.expectedBisections = 14
		cfg.expectedMerges = 2
		hook := test.NewGlobal()
		runChallengeIntegrationTest(t, hook, cfg)
		AssertLogsContain(t, hook, "Reached one-step-fork at 3")
		AssertLogsContain(t, hook, "Reached one-step-fork at 3")
	})
}

type challengeProtocolTestConfig struct {
	// The latest heights by index at the assertion chain level.
	aliceHeight uint64
	bobHeight   uint64
	// The height in the assertion chain at which the validators diverge.
	assertionDivergenceHeight uint64
	// The number of big steps of WAVM opcodes at the one-step-fork point in a test.
	numBigStepsAtAssertionHeight uint64
	// The heights at which the validators diverge in histories at the big step
	// subchallenge level.
	bigStepDivergenceHeight uint64
	// The number of WAVM opcodes (small steps) at the one-step-fork point of a big step
	// subchallenge in a test.
	numSmallStepsAtBigStep uint64
	// The heights at which the validators diverge in histories at the small step
	// subchallenge level.
	smallStepDivergenceHeight uint64
	currentChainHeight        uint64
	// Events we want to assert are fired from the goimpl.
	expectedBisections    uint64
	expectedMerges        uint64
	expectedVerticesAdded uint64
}

func prepareHonestStates(
	t testing.TB,
	ctx context.Context,
	chain protocol.Protocol,
	backend *backends.SimulatedBackend,
	chainHeight uint64,
	prevInboxMaxCount *big.Int,
) ([]*protocol.ExecutionState, []*big.Int) {
	t.Helper()
	// Initialize each validator's associated state roots which diverge
	var genesis protocol.Assertion
	err := chain.Call(func(tx protocol.ActiveTx) error {
		genesisAssertion, err := chain.AssertionBySequenceNum(ctx, tx, 0)
		require.NoError(t, err)
		genesis = genesisAssertion
		return nil
	})
	require.NoError(t, err)

	genesisState := &protocol.ExecutionState{
		GlobalState: protocol.GoGlobalState{
			BlockHash: common.Hash{},
		},
		MachineStatus: protocol.MachineStatusFinished,
	}
	genesisStateHash := protocol.ComputeStateHash(genesisState, prevInboxMaxCount)
	require.Equal(t, genesisStateHash, genesis.StateHash(), "Genesis state hash unequal")

	// Initialize each validator associated state roots which diverge
	// at specified points in the test config.
	honestStates := make([]*protocol.ExecutionState, chainHeight)
	honestInboxCounts := make([]*big.Int, chainHeight)
	honestStates[0] = genesisState
	honestInboxCounts[0] = big.NewInt(1)

	var honestBlockHash common.Hash
	for i := uint64(1); i < chainHeight; i++ {
		honestBlockHash = backend.Commit()
		state := &protocol.ExecutionState{
			GlobalState: protocol.GoGlobalState{
				BlockHash: honestBlockHash,
				Batch:     1,
			},
			MachineStatus: protocol.MachineStatusFinished,
		}

		honestStates[i] = state
		honestInboxCounts[i] = big.NewInt(1)
	}
	return honestStates, honestInboxCounts
}

func prepareMaliciousStates(
	t testing.TB,
	cfg *challengeProtocolTestConfig,
	honestStates []*protocol.ExecutionState,
	honestInboxCounts []*big.Int,
	prevInboxMaxCount *big.Int,
) ([]*protocol.ExecutionState, []*big.Int) {
	divergenceHeight := cfg.assertionDivergenceHeight
	numRoots := cfg.bobHeight + 1
	states := make([]*protocol.ExecutionState, numRoots)
	inboxCounts := make([]*big.Int, numRoots)

	for j := uint64(0); j < numRoots; j++ {
		if divergenceHeight == 0 || j < divergenceHeight {
			states[j] = honestStates[j]
			inboxCounts[j] = honestInboxCounts[j]
		} else {
			junkRoot := make([]byte, 32)
			_, err := rand.Read(junkRoot)
			require.NoError(t, err)
			blockHash := crypto.Keccak256Hash(junkRoot)
			evilState := &protocol.ExecutionState{
				GlobalState: protocol.GoGlobalState{
					BlockHash: blockHash,
					Batch:     1,
				},
				MachineStatus: protocol.MachineStatusFinished,
			}
			states[j] = evilState
			inboxCounts[j] = big.NewInt(1)
		}
	}
	return states, inboxCounts
}

func runChallengeIntegrationTest(t testing.TB, hook *test.Hook, cfg *challengeProtocolTestConfig) {
	ctx := context.Background()
	ref := util.NewRealTimeReference()
	chains, accs, addrs, backend := setupAssertionChains(t, 3) // 0th is admin chain.
	prevInboxMaxCount := big.NewInt(1)

	// Advance the chain by 100 blocks as there needs to be a minimum period of time
	// before any assertions can be made on-chain.
	for i := 0; i < 100; i++ {
		backend.Commit()
	}

	honestStates, honestInboxCounts := prepareHonestStates(
		t,
		ctx,
		chains[1],
		backend,
		cfg.currentChainHeight,
		prevInboxMaxCount,
	)

	maliciousStates, maliciousInboxCounts := prepareMaliciousStates(
		t,
		cfg,
		honestStates,
		honestInboxCounts,
		prevInboxMaxCount,
	)

	// Initialize each validator.
	honestManager, err := statemanager.NewWithAssertionStates(
		honestStates,
		honestInboxCounts,
	)
	require.NoError(t, err)
	aliceAddr := accs[1].accountAddr
	alice, err := New(
		ctx,
		chains[1], // Chain 0 is reserved for admin controls.
		backend,
		honestManager,
		addrs.Rollup,
		WithName("alice"),
		WithAddress(aliceAddr),
		WithDisableLeafCreation(),
		WithTimeReference(ref),
		WithChallengeVertexWakeInterval(time.Millisecond*10),
	)
	require.NoError(t, err)

	maliciousManager, err := statemanager.NewWithAssertionStates(
		maliciousStates,
		maliciousInboxCounts,
	)
	require.NoError(t, err)
	bobAddr := accs[1].accountAddr
	bob, err := New(
		ctx,
		chains[2], // Chain 0 is reserved for admin controls.
		backend,
		maliciousManager,
		addrs.Rollup,
		WithName("bob"),
		WithAddress(bobAddr),
		WithDisableLeafCreation(),
		WithTimeReference(ref),
		WithChallengeVertexWakeInterval(time.Millisecond*10),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	// We fire off each validator's background routines.
	go alice.Start(ctx)
	go bob.Start(ctx)

	var managerAddr common.Address
	err = chains[1].Call(func(tx protocol.ActiveTx) error {
		manager, err := chains[1].CurrentChallengeManager(ctx, tx)
		require.NoError(t, err)
		managerAddr = manager.Address()
		return nil
	})
	require.NoError(t, err)

	var totalVertexAdded uint64
	var totalBisections uint64
	var totalMerges uint64

	go func() {
		logs := make(chan types.Log, 100)
		query := ethereum.FilterQuery{
			Addresses: []common.Address{managerAddr},
		}
		sub, err := backend.SubscribeFilterLogs(ctx, query, logs)
		require.NoError(t, err)
		defer sub.Unsubscribe()
		for {
			select {
			case err := <-sub.Err():
				log.Fatal(err)
			case <-ctx.Done():
				return
			case vLog := <-logs:
				if len(vLog.Topics) == 0 {
					continue
				}
				topic := vLog.Topics[0]
				switch {
				case bytes.Equal(topic[:], vertexAddedEventSig):
					totalVertexAdded++
				case bytes.Equal(topic[:], bisectEventSig):
					totalBisections++
				case bytes.Equal(topic[:], mergeEventSig):
					totalMerges++
				default:
				}
			}
		}
	}()

	time.Sleep(time.Millisecond * 100)

	// Submit leaf creation manually for each validator.
	_, err = alice.SubmitLeafCreation(ctx)
	require.NoError(t, err)
	_, err = bob.SubmitLeafCreation(ctx)
	require.NoError(t, err)
	AssertLogsContain(t, hook, "Submitted assertion")

	<-ctx.Done()
	assert.Equal(t, cfg.expectedVerticesAdded, totalVertexAdded, "Did not get expected challenge leaf creations")
	assert.Equal(t, cfg.expectedBisections, totalBisections, "Did not get expected total bisections")
	assert.Equal(t, cfg.expectedMerges, totalMerges, "Did not get expected total merges")
}
