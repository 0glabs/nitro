package validator

import (
	"context"
	"fmt"

	"github.com/OffchainLabs/challenge-protocol-v2/protocol"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func (v *vertexTracker) determineBisectionPointWithHistory(
	ctx context.Context,
	parentHeight,
	toHeight uint64,
) (util.HistoryCommitment, error) {
	bisectTo, err := util.BisectionPoint(parentHeight, toHeight)
	if err != nil {
		return util.HistoryCommitment{}, errors.Wrapf(err, "determining bisection point failed for %d and %d", parentHeight, toHeight)
	}
	historyCommit, err := v.cfg.stateManager.HistoryCommitmentUpTo(ctx, bisectTo)
	if err != nil {
		return util.HistoryCommitment{}, errors.Wrapf(err, "could not rertieve history commitment up to height %d", bisectTo)
	}
	return historyCommit, nil
}

// Performs a bisection move during a BlockChallenge in the assertion protocol given
// a validator challenge vertex. It will create a historical commitment for the vertex
// the validator wants to bisect to and an associated proof for submitting to the goimpl.
func (v *vertexTracker) bisect(
	ctx context.Context,
	validatorChallengeVertex protocol.ChallengeVertex,
) (protocol.ChallengeVertex, error) {
	var bisectedVertex protocol.ChallengeVertex
	var isPresumptive bool

	commitment := validatorChallengeVertex.HistoryCommitment()
	toHeight := commitment.Height
	prev, err := validatorChallengeVertex.Prev(ctx)
	if err != nil {
		return nil, err
	}
	prevCommitment := prev.Unwrap().HistoryCommitment()
	parentHeight := prevCommitment.Height

	historyCommit, err := v.determineBisectionPointWithHistory(ctx, parentHeight, toHeight)
	if err != nil {
		return nil, err
	}
	bisectTo := historyCommit.Height
	proof, err := v.cfg.stateManager.PrefixProof(ctx, bisectTo, toHeight)
	if err != nil {
		return nil, errors.Wrapf(err, "generating prefix proof failed from height %d to %d", bisectTo, toHeight)
	}
	bisected, err := validatorChallengeVertex.Bisect(ctx, historyCommit, proof)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			"%s could not bisect to height=%d,commit=%s from height=%d,commit=%s",
			v.cfg.validatorName,
			bisectTo,
			util.Trunc(historyCommit.Merkle.Bytes()),
			validatorChallengeVertex.HistoryCommitment().Height,
			util.Trunc(validatorChallengeVertex.HistoryCommitment().Merkle.Bytes()),
		)
	}
	bisectedVertex = bisected
	bisectedVertexIsPresumptiveSuccessor, err := bisectedVertex.IsPresumptiveSuccessor(ctx)
	if err != nil {
		return nil, err
	}
	isPresumptive = bisectedVertexIsPresumptiveSuccessor
	bisectedVertexCommitment := bisectedVertex.HistoryCommitment()
	log.WithFields(logrus.Fields{
		"name":               v.cfg.validatorName,
		"isPs":               isPresumptive,
		"bisectedFrom":       validatorChallengeVertex.HistoryCommitment().Height,
		"bisectedFromMerkle": util.Trunc(validatorChallengeVertex.HistoryCommitment().Merkle.Bytes()),
		"bisectedTo":         bisectedVertexCommitment.Height,
		"bisectedToMerkle":   util.Trunc(bisectedVertexCommitment.Merkle[:]),
	}).Info("Successfully bisected to vertex")
	return bisectedVertex, nil
}

// Performs a merge move during a BlockChallenge in the assertion protocol given
// a challenge vertex and the sequence number we should be merging into. To do this, we
// also need to fetch vertex we are merging to by reading it from the goimpl.
func (v *vertexTracker) merge(
	ctx context.Context,
	challengeCommitHash protocol.ChallengeHash,
	mergingTo protocol.ChallengeVertex,
	mergingFrom protocol.ChallengeVertex,
) (protocol.ChallengeVertex, error) {
	currentCommit := mergingFrom.HistoryCommitment()
	mergingToCommit := mergingTo.HistoryCommitment()
	mergingToHeight := mergingToCommit.Height
	if mergingToHeight >= currentCommit.Height {
		return nil, fmt.Errorf(
			"merging to height %d cannot be >= vertex height %d",
			mergingToHeight,
			currentCommit.Height,
		)
	}
	historyCommit, err := v.cfg.stateManager.HistoryCommitmentUpTo(ctx, mergingToHeight)
	if err != nil {
		return nil, err
	}
	proof, err := v.cfg.stateManager.PrefixProof(ctx, mergingToHeight, currentCommit.Height)
	if err != nil {
		return nil, err
	}
	mergedTo, err := mergingFrom.Merge(ctx, historyCommit, proof)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			"%s could not merge vertex at height=%d,commit=%s to height%d,commit=%s",
			v.cfg.validatorName,
			currentCommit.Height,
			util.Trunc(currentCommit.Merkle.Bytes()),
			mergingToHeight,
			util.Trunc(mergingToCommit.Merkle.Bytes()),
		)
	}
	log.WithFields(logrus.Fields{
		"name":             v.cfg.validatorName,
		"mergedFrom":       mergingFrom.HistoryCommitment().Height,
		"mergedFromMerkle": util.Trunc(mergingFrom.HistoryCommitment().Merkle.Bytes()),
		"mergedTo":         mergingToCommit.Height,
		"mergedToMerkle":   util.Trunc(mergingToCommit.Merkle[:]),
	}).Info("Successfully merged to vertex")
	return mergedTo, nil
}
