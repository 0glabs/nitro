package validator

import (
	"context"
	"testing"

	"github.com/OffchainLabs/challenge-protocol-v2/protocol"
	statemanager "github.com/OffchainLabs/challenge-protocol-v2/state-manager"
	"github.com/OffchainLabs/challenge-protocol-v2/testing/mocks"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

func Test_bisect(t *testing.T) {
	ctx := context.Background()
	t.Run("bad bisection points", func(t *testing.T) {
		createdData := createTwoValidatorFork(t, ctx, &createForkConfig{
			divergeHeight: 10,
			numBlocks:     100,
		})
		validator, err := New(
			ctx,
			createdData.assertionChains[1],
			createdData.backend,
			&mocks.MockStateManager{},
			createdData.addrs.Rollup,
		)
		require.NoError(t, err)

		vertex := &mocks.MockChallengeVertex{
			MockPrev: util.Some(protocol.ChallengeVertex(&mocks.MockChallengeVertex{
				MockHistory: util.HistoryCommitment{
					Height: 3,
					Merkle: common.BytesToHash([]byte{0}),
				},
			})),
			MockHistory: util.HistoryCommitment{
				Height: 0,
				Merkle: common.BytesToHash([]byte{1}),
			},
		}
		v := vertexTracker{
			cfg: &vertexTrackerConfig{
				chain:            validator.chain,
				stateManager:     validator.stateManager,
				validatorName:    validator.name,
				validatorAddress: validator.address,
			},
		}
		_, err = v.bisect(ctx, vertex)
		require.ErrorContains(t, err, "determining bisection point failed")
	})
	t.Run("bisects", func(t *testing.T) {
		logsHook := test.NewGlobal()
		createdData := createTwoValidatorFork(t, ctx, &createForkConfig{
			divergeHeight: 8,
			numBlocks:     63,
		})

		honestManager := statemanager.New(createdData.honestValidatorStateRoots)
		honestValidator, err := New(
			ctx,
			createdData.assertionChains[1],
			createdData.backend,
			honestManager,
			createdData.addrs.Rollup,
		)
		require.NoError(t, err)

		evilManager := statemanager.New(createdData.evilValidatorStateRoots)
		evilValidator, err := New(
			ctx,
			createdData.assertionChains[2],
			createdData.backend,
			evilManager,
			createdData.addrs.Rollup,
		)
		require.NoError(t, err)

		bisectedTo := runBisectionTest(
			t,
			logsHook,
			ctx,
			honestValidator,
			evilValidator,
			createdData.leaf1,
			createdData.leaf2,
		)

		// Expect to bisect to 31.
		commitment, err := bisectedTo.HistoryCommitment(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(31), commitment.Height)
	})
}

func Test_merge(t *testing.T) {
	ctx := context.Background()
	t.Run("OK", func(t *testing.T) {
		logsHook := test.NewGlobal()
		createdData := createTwoValidatorFork(t, ctx, &createForkConfig{
			divergeHeight: 32,
			numBlocks:     63,
		})

		honestManager := statemanager.New(createdData.honestValidatorStateRoots)
		honestValidator, err := New(
			ctx,
			createdData.assertionChains[1],
			createdData.backend,
			honestManager,
			createdData.addrs.Rollup,
		)
		require.NoError(t, err)

		evilManager := statemanager.New(createdData.evilValidatorStateRoots)
		evilValidator, err := New(
			ctx,
			createdData.assertionChains[2],
			createdData.backend,
			evilManager,
			createdData.addrs.Rollup,
		)
		require.NoError(t, err)

		bisectedTo := runBisectionTest(
			t,
			logsHook,
			ctx,
			honestValidator,
			evilValidator,
			createdData.leaf1,
			createdData.leaf2,
		)

		// Both validators should have the same history upon which one will try to merge into.
		require.Equal(t, createdData.evilValidatorStateRoots[31], createdData.honestValidatorStateRoots[31], "Different state root at 64")
		createdDataLeaf1Height, err := createdData.leaf1.Height()
		require.NoError(t, err)
		mergingFromHistory, err := honestValidator.stateManager.HistoryCommitmentUpTo(ctx, createdDataLeaf1Height)
		require.NoError(t, err)

		// Get the vertex we want to merge from.
		var vertexToMergeFrom protocol.ChallengeVertex
		var challengeId protocol.ChallengeHash
		genesisId, err := honestValidator.chain.GetAssertionId(ctx, protocol.AssertionSequenceNumber(0))
		require.NoError(t, err)
		manager, err := honestValidator.chain.CurrentChallengeManager(ctx)
		require.NoError(t, err)
		chalId, err := manager.CalculateChallengeHash(ctx, common.Hash(genesisId), protocol.BlockChallenge)
		require.NoError(t, err)

		challengeId = chalId

		vertexId, err := manager.CalculateChallengeVertexId(ctx, chalId, mergingFromHistory)
		require.NoError(t, err)

		mergingFromV, err := manager.GetVertex(ctx, vertexId)
		require.NoError(t, err)
		vertexToMergeFrom = mergingFromV.Unwrap()

		// Perform a merge move to the bisected vertex from an origin.
		v := vertexTracker{
			cfg: &vertexTrackerConfig{
				chain:            honestValidator.chain,
				stateManager:     honestValidator.stateManager,
				validatorName:    honestValidator.name,
				validatorAddress: honestValidator.address,
			},
		}
		mergingTo, err := v.merge(ctx, challengeId, bisectedTo, vertexToMergeFrom)
		require.NoError(t, err)
		AssertLogsContain(t, logsHook, "Successfully merged to vertex")
		require.Equal(t, bisectedTo.Id(), mergingTo.Id())
	})
}

func runBisectionTest(
	t *testing.T,
	logsHook *test.Hook,
	ctx context.Context,
	honestValidator,
	evilValidator *Validator,
	leaf1,
	leaf2 protocol.Assertion,
) protocol.ChallengeVertex {
	err := honestValidator.onLeafCreated(ctx,  leaf1)
	require.NoError(t, err)
	err = honestValidator.onLeafCreated(ctx,  leaf2)
	require.NoError(t, err)
	AssertLogsContain(t, logsHook, "New assertion appended")
	AssertLogsContain(t, logsHook, "New assertion appended")
	AssertLogsContain(t, logsHook, "Successfully created challenge and added leaf")

	var vertexToBisect protocol.ChallengeVertex
	var chalId protocol.ChallengeHash

	genesisId, err := evilValidator.chain.GetAssertionId(ctx, protocol.AssertionSequenceNumber(0))
	require.NoError(t, err)
	manager, err := evilValidator.chain.CurrentChallengeManager(ctx)
	require.NoError(t, err)
	chalIdComputed, err := manager.CalculateChallengeHash(ctx, common.Hash(genesisId), protocol.BlockChallenge)
	require.NoError(t, err)

	chalId = chalIdComputed

	challenge, err := manager.GetChallenge(ctx, chalId)
	require.NoError(t, err)
	require.Equal(t, false, challenge.IsNone())
	assertion, err := evilValidator.chain.AssertionBySequenceNum(ctx, protocol.AssertionSequenceNumber(2))
	require.NoError(t, err)

	assertionHeight, err := assertion.Height()
		require.NoError(t, err)
		honestCommit, err := evilValidator.stateManager.HistoryCommitmentUpTo(ctx, assertionHeight)
	require.NoError(t, err)
	vToBisect, err := challenge.Unwrap().AddBlockChallengeLeaf(ctx, assertion, honestCommit)
	require.NoError(t, err)
	vertexToBisect = vToBisect

	// Check presumptive statuses.
	isPs, err := vertexToBisect.IsPresumptiveSuccessor(ctx)
	require.NoError(t, err)
	require.Equal(t, false, isPs)

	v := vertexTracker{
		cfg: &vertexTrackerConfig{
			chain:            evilValidator.chain,
			stateManager:     evilValidator.stateManager,
			validatorName:    evilValidator.name,
			validatorAddress: evilValidator.address,
		},
	}

	bisectedVertex, err := v.bisect(ctx,  vertexToBisect)
	require.NoError(t, err)

	bisectedVertexHistoryCommitment, err := bisectedVertex.HistoryCommitment(ctx)
	require.NoError(t, err)
	shouldBisectToCommit, err := evilValidator.stateManager.HistoryCommitmentUpTo(ctx, bisectedVertexHistoryCommitment.Height)
	require.NoError(t, err)

	commitment, err := bisectedVertex.HistoryCommitment(ctx)
	require.NoError(t, err)
	require.Equal(t, commitment.Hash(), shouldBisectToCommit.Hash())

	AssertLogsContain(t, logsHook, "Successfully bisected to vertex")
	return bisectedVertex
}
