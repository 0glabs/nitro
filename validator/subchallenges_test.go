package validator

import (
	"context"
	"testing"

	"github.com/OffchainLabs/challenge-protocol-v2/protocol"
	statemanager "github.com/OffchainLabs/challenge-protocol-v2/state-manager"
	"github.com/stretchr/testify/require"
)

func TestFullChallengeResolution(t *testing.T) {
	ctx := context.Background()

	// Start by creating a simple, two validator fork in the assertion
	// chain at height 1.
	createdData := createTwoValidatorFork(t, ctx, &createForkConfig{
		numBlocks:     1,
		divergeHeight: 1,
	})
	t.Log("Alice (honest) and Bob have a fork at height 1")
	// TODO: Customize the statemanager to allow fixed num steps.
	honestManager := statemanager.New(createdData.honestValidatorStateRoots)
	evilManager := statemanager.New(createdData.evilValidatorStateRoots)

	// Next, we create a challenge.
	honestChain := createdData.assertionChains[1]
	honestChain.Tx(func(tx protocol.ActiveTx) error {
		chal, err := honestChain.CreateSuccessionChallenge(ctx, tx, 0)
		require.NoError(t, err)

		require.Equal(t, protocol.BlockChallenge, chal.GetType())
		t.Log("Created BlockChallenge")

		commit1, err := honestManager.HistoryCommitmentUpTo(ctx, createdData.leaf1.Height())
		require.NoError(t, err)
		commit2, err := evilManager.HistoryCommitmentUpTo(ctx, createdData.leaf2.Height())
		require.NoError(t, err)

		vertex1, err := chal.AddBlockChallengeLeaf(ctx, tx, createdData.leaf1, commit1)
		require.NoError(t, err)
		t.Log("Alice (honest) added leaf at height 1")
		vertex2, err := chal.AddBlockChallengeLeaf(ctx, tx, createdData.leaf2, commit2)
		require.NoError(t, err)
		t.Log("Bob added leaf at height 1")

		parentVertex, err := chal.RootVertex(ctx, tx)
		require.NoError(t, err)

		areAtOSF, err := parentVertex.ChildrenAreAtOneStepFork(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, true, areAtOSF, "Children not at one-step fork")

		t.Log("Alice and Bob's BlockChallenge vertices that are at a one-step-fork")

		subChal, err := parentVertex.CreateSubChallenge(ctx, tx)
		require.NoError(t, err)

		require.Equal(t, protocol.BigStepChallenge, subChal.GetType())
		t.Log("Created BigStepChallenge")

		commit1, err = honestManager.BigStepLeafCommitment(ctx, 0, 1)
		require.NoError(t, err)
		commit2, err = evilManager.BigStepLeafCommitment(ctx, 0, 1)
		require.NoError(t, err)

		vertex1, err = subChal.AddSubChallengeLeaf(ctx, tx, vertex1, commit1)
		require.NoError(t, err)
		t.Log("Alice (honest) added leaf at height 1")
		vertex2, err = subChal.AddSubChallengeLeaf(ctx, tx, vertex2, commit2)
		require.NoError(t, err)
		t.Log("Bob added leaf at height 1")

		parentVertex, err = subChal.RootVertex(ctx, tx)
		require.NoError(t, err)

		areAtOSF, err = parentVertex.ChildrenAreAtOneStepFork(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, true, areAtOSF, "Children in BigStepChallenge not at one-step fork")

		t.Log("Alice and Bob's BigStepChallenge vertices are at a one-step-fork")

		subChal, err = parentVertex.CreateSubChallenge(ctx, tx)
		require.NoError(t, err)

		require.Equal(t, protocol.SmallStepChallenge, subChal.GetType())
		t.Log("Created SmallStepChallenge")

		commit1, err = honestManager.SmallStepLeafCommitment(ctx, 0, 1)
		require.NoError(t, err)
		commit2, err = evilManager.SmallStepLeafCommitment(ctx, 0, 1)
		require.NoError(t, err)

		vertex1, err = subChal.AddSubChallengeLeaf(ctx, tx, vertex1, commit1)
		require.NoError(t, err)
		t.Log("Alice (honest) added leaf at height 1")
		vertex2, err = subChal.AddSubChallengeLeaf(ctx, tx, vertex2, commit2)
		require.NoError(t, err)
		t.Log("Bob added leaf at height 1")

		parentVertex, err = subChal.RootVertex(ctx, tx)
		require.NoError(t, err)

		areAtOSF, err = parentVertex.ChildrenAreAtOneStepFork(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, true, areAtOSF, "Children in SmallStepChallenge not at one-step fork")

		t.Log("Alice and Bob's BigStepChallenge vertices are at a one-step-fork")
		t.Log("Reached one-step-proof in SmallStepChallenge")
		t.Log("Alice wins")
		return nil
	})
}
