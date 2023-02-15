// Package solimpl includes an easy-to-use abstraction
// around the challenge protocol contracts using their Go
// bindings and exposes minimal details of Ethereum's internals.
package solimpl

import (
	"bytes"
	"context"
	"math/big"
	"strings"
	"time"

	"fmt"
	"github.com/OffchainLabs/challenge-protocol-v2/solgen/go/rollupgen"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

var (
	ErrUnconfirmedParent   = errors.New("parent assertion is not confirmed")
	ErrNonPendingAssertion = errors.New("assertion is not pending")
	ErrRejectedAssertion   = errors.New("assertion already rejected")
	ErrInvalidChildren     = errors.New("invalid children")
	ErrNotFound            = errors.New("item not found on-chain")
	ErrAlreadyExists       = errors.New("item already exists on-chain")
	ErrPrevDoesNotExist    = errors.New("assertion predecessor does not exist")
	ErrTooLate             = errors.New("too late to create assertion sibling")
	ErrInvalidHeight       = errors.New("invalid assertion height")
	uint256Ty, _           = abi.NewType("uint256", "", nil)
	hashTy, _              = abi.NewType("bytes32", "", nil)
)

// ChainCommitter defines a type of chain backend that supports
// committing changes via a direct method, such as a simulated backend
// for testing purposes.
type ChainCommiter interface {
	Commit() common.Hash
}

// AssertionChain is a wrapper around solgen bindings
// that implements the protocol interface.
type AssertionChain struct {
	backend    bind.ContractBackend
	caller     *rollupgen.RollupCoreCaller
	writer     *rollupgen.RollupUserLogicTransactor
	callOpts   *bind.CallOpts
	txOpts     *bind.TransactOpts
	stakerAddr common.Address
}

// NewAssertionChain instantiates an assertion chain
// instance from a chain backend and provided options.
func NewAssertionChain(
	ctx context.Context,
	rollupAddr common.Address,
	rollupUserLogicAddr common.Address,
	txOpts *bind.TransactOpts,
	callOpts *bind.CallOpts,
	stakerAddr common.Address,
	backend bind.ContractBackend,
) (*AssertionChain, error) {
	chain := &AssertionChain{
		backend:    backend,
		callOpts:   callOpts,
		txOpts:     txOpts,
		stakerAddr: stakerAddr,
	}
	coreBinding, err := rollupgen.NewRollupCore(
		rollupAddr, chain.backend,
	)
	if err != nil {
		return nil, err
	}
	assertionChainBinding, err := rollupgen.NewRollupUserLogic(
		rollupUserLogicAddr, chain.backend,
	)
	if err != nil {
		return nil, err
	}
	chain.caller = &coreBinding.RollupCoreCaller
	chain.writer = &assertionChainBinding.RollupUserLogicTransactor
	return chain, nil
}

// ChallengePeriodSeconds
func (ac *AssertionChain) ChallengePeriodSeconds() (time.Duration, error) {
	manager, err := ac.ChallengeManager()
	if err != nil {
		return time.Second, err
	}
	res, err := manager.caller.ChallengePeriodSec(ac.callOpts)
	if err != nil {
		return time.Second, err
	}
	return time.Second * time.Duration(res.Uint64()), nil
}

// AssertionByID --
func (ac *AssertionChain) AssertionByID(assertionNum uint64) (*Assertion, error) {
	res, err := ac.caller.GetAssertion(ac.callOpts, assertionNum)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(res.StateHash[:], make([]byte, 32)) {
		return nil, errors.Wrapf(
			ErrNotFound,
			"assertion with id %d",
			assertionNum,
		)
	}
	return &Assertion{
		id:    assertionNum,
		chain: ac,
		inner: res,
		StateCommitment: util.StateCommitment{
			Height:    res.Height.Uint64(),
			StateRoot: res.StateHash,
		},
	}, nil
}

// CreateAssertion makes an on-chain claim given a previous assertion state, a commitment.
func (ac *AssertionChain) CreateAssertion(
	height uint64,
	prevAssertionId uint64,
	prevAssertionState *ExecutionState,
	postState *ExecutionState,
	prevInboxMaxCount *big.Int,
) (*Assertion, error) {
	prev, err := ac.AssertionByID(prevAssertionId)
	if err != nil {
		return nil, errors.Wrapf(err, "could not get prev assertion with id: %d", prevAssertionId)
	}
	fmt.Printf("%+v\n", prev)
	prevHeight := prev.inner.Height.Uint64()
	if prevHeight >= height {
		return nil, errors.Wrapf(ErrInvalidHeight, "prev height %d was >= incoming %d", prevHeight, height)
	}
	numBlocks := height - prevHeight
	result := withChainCommitment(ac.backend, func() error {
		_, stakeErr := ac.writer.NewStakeOnNewAssertion(
			ac.txOpts,
			rollupgen.AssertionInputs{
				BeforeState: prevAssertionState.AsSolidityStruct(),
				AfterState:  postState.AsSolidityStruct(),
				NumBlocks:   numBlocks,
			},
			common.Hash{}, // Expected hash.
			prevInboxMaxCount,
		)
		return stakeErr
	})
	if createErr := handleCreateAssertionError(result, height, postState.GlobalState.BlockHash); createErr != nil {
		return nil, createErr
	}
	return ac.AssertionByID(prevAssertionId + 1)
}

// CreateSuccessionChallenge creates a succession challenge
func (ac *AssertionChain) CreateSuccessionChallenge(assertionId uint64) (*Challenge, error) {
	err := withChainCommitment(ac.backend, func() error {
		_, err2 := ac.writer.CreateChallenge(
			ac.txOpts,
			assertionId,
		)
		return err2
	})
	if err3 := handleCreateSuccessionChallengeError(err, assertionId); err3 != nil {
		return nil, err3
	}
	manager, err := ac.ChallengeManager()
	if err != nil {
		return nil, err
	}
	challengeId, err := manager.CalculateChallengeId(common.Hash{}, BlockChallenge)
	if err != nil {
		return nil, err
	}
	return manager.ChallengeByID(challengeId)
}

// Confirm creates a confirmation for the given assertion.
func (a *Assertion) Confirm() error {
	_, err := a.chain.writer.ConfirmNextAssertion(a.chain.txOpts, common.Hash{}, common.Hash{})
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "Assertion does not exist"):
		return errors.Wrapf(ErrNotFound, "assertion with id %#x", a.id)
	case strings.Contains(err.Error(), "Previous assertion not confirmed"):
		return errors.Wrapf(ErrUnconfirmedParent, "previous assertion not confirmed")
	default:
		return err
	}
}

// Reject creates a rejection for the given assertion.
func (a *Assertion) Reject() error {
	_, err := a.chain.writer.RejectNextAssertion(a.chain.txOpts, a.chain.stakerAddr)
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "Assertion does not exist"):
		return errors.Wrapf(ErrNotFound, "assertion with id %#x", a.id)
	case strings.Contains(err.Error(), "Assertion is not pending"):
		return errors.Wrapf(ErrNonPendingAssertion, "assertion with id %#x", a.id)
	default:
		return err
	}
}

func handleCreateSuccessionChallengeError(err error, assertionId uint64) error {
	if err == nil {
		return nil
	}
	errS := err.Error()
	switch {
	case strings.Contains(errS, "Assertion does not exist"):
		return errors.Wrapf(ErrNotFound, "assertion id %d", assertionId)
	case strings.Contains(errS, "Assertion already rejected"):
		return errors.Wrapf(ErrRejectedAssertion, "assertion id %d", assertionId)
	case strings.Contains(errS, "Challenge already created"):
		return errors.Wrapf(ErrAlreadyExists, "assertion id %d", assertionId)
	case strings.Contains(errS, "At least two children not created"):
		return errors.Wrapf(ErrInvalidChildren, "assertion id %d", assertionId)
	case strings.Contains(errS, "Too late to challenge"):
		return errors.Wrapf(ErrTooLate, "assertion id %d", assertionId)
	default:
		return err
	}
}

func handleCreateAssertionError(err error, height uint64, blockHash common.Hash) error {
	if err == nil {
		return nil
	}
	errS := err.Error()
	switch {
	case strings.Contains(errS, "Assertion already exists"):
		return errors.Wrapf(
			ErrAlreadyExists,
			"commit block hash %#x and height %d",
			blockHash,
			height,
		)
	case strings.Contains(errS, "Height not greater than predecessor"):
		return errors.Wrapf(
			ErrInvalidHeight,
			"commit block hash %#x and height %d",
			blockHash,
			height,
		)
	case strings.Contains(errS, "Previous assertion does not exist"):
		return ErrPrevDoesNotExist
	case strings.Contains(errS, "Too late to create sibling"):
		return ErrTooLate
	default:
		return err
	}
}

// Runs a callback function meant to write to a contract backend, and if the
// chain backend supports committing directly, we call the commit function before
// returning.
func withChainCommitment(backend bind.ContractBackend, fn func() error) error {
	if err := fn(); err != nil {
		return err
	}
	if commiter, ok := backend.(ChainCommiter); ok {
		commiter.Commit()
	}
	return nil
}

func copyTxOpts(opts *bind.TransactOpts) *bind.TransactOpts {
	return &bind.TransactOpts{
		From:      opts.From,
		Nonce:     opts.Nonce,
		Signer:    opts.Signer,
		Value:     opts.Value,
		GasPrice:  opts.GasPrice,
		GasFeeCap: opts.GasFeeCap,
		GasTipCap: opts.GasTipCap,
		GasLimit:  opts.GasLimit,
		Context:   opts.Context,
		NoSend:    opts.NoSend,
	}
}
