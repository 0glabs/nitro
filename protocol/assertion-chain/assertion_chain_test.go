package assertionchain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/OffchainLabs/challenge-protocol-v2/solgen/go/outgen"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/backends"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestCreateAssertion(t *testing.T) {
	ctx := context.Background()
	acc, err := setupAccount()
	require.NoError(t, err)

	genesisStateRoot := common.BytesToHash([]byte("foo"))
	addr, _, _, err := outgen.DeployAssertionChain(
		acc.txOpts,
		acc.backend,
		genesisStateRoot,
		big.NewInt(10), // 10 second challenge period.
	)
	require.NoError(t, err)

	acc.backend.Commit()

	chain, err := NewAssertionChain(
		ctx, addr, acc.txOpts, &bind.CallOpts{}, acc.accountAddr, acc.backend,
	)
	require.NoError(t, err)

	commit := util.StateCommitment{
		Height:    1,
		StateRoot: common.BytesToHash([]byte{1}),
	}
	genesisId := common.Hash{}

	t.Run("OK", func(t *testing.T) {
		err = chain.createAssertion(commit, genesisId)
		require.NoError(t, err)

		acc.backend.Commit()

		id := getAssertionId(commit, genesisId)
		created, err2 := chain.AssertionByID(id)
		require.NoError(t, err2)
		require.Equal(t, commit.StateRoot[:], created.inner.StateHash[:])
	})
	t.Run("already exists", func(t *testing.T) {
		err = chain.createAssertion(commit, genesisId)
		require.ErrorIs(t, err, ErrAlreadyExists)
	})
	t.Run("previous assertion does not exist", func(t *testing.T) {
		commit := util.StateCommitment{
			Height:    2,
			StateRoot: common.BytesToHash([]byte{2}),
		}
		err = chain.createAssertion(commit, common.BytesToHash([]byte("nyan")))
		require.ErrorIs(t, err, ErrPrevDoesNotExist)
	})
	t.Run("invalid height", func(t *testing.T) {
		commit := util.StateCommitment{
			Height:    0,
			StateRoot: common.BytesToHash([]byte{3}),
		}
		err = chain.createAssertion(commit, genesisId)
		require.ErrorIs(t, err, ErrInvalidHeight)
	})
	t.Run("too late to create sibling", func(t *testing.T) {
		// Adds two challenge periods to the chain timestamp.
		err = acc.backend.AdjustTime(time.Second * 20)
		require.NoError(t, err)
		commit := util.StateCommitment{
			Height:    1,
			StateRoot: common.BytesToHash([]byte("forked")),
		}
		err = chain.createAssertion(commit, genesisId)
		require.ErrorIs(t, err, ErrTooLate)
	})
}

func TestAssertionByID(t *testing.T) {
	ctx := context.Background()
	acc, err := setupAccount()
	require.NoError(t, err)
	genesisStateRoot := common.BytesToHash([]byte("foo"))
	addr, _, _, err := outgen.DeployAssertionChain(
		acc.txOpts,
		acc.backend,
		genesisStateRoot,
		big.NewInt(1), // 1 second challenge period.
	)
	require.NoError(t, err)

	acc.backend.Commit()

	chain, err := NewAssertionChain(
		ctx, addr, acc.txOpts, &bind.CallOpts{}, acc.accountAddr, acc.backend,
	)
	require.NoError(t, err)

	genesisId := common.Hash{}
	resp, err := chain.AssertionByID(genesisId)
	require.NoError(t, err)

	require.Equal(t, genesisStateRoot[:], resp.inner.StateHash[:])

	_, err = chain.AssertionByID(common.BytesToHash([]byte("bar")))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestChallengePeriodSeconds(t *testing.T) {
	ctx := context.Background()
	acc, err := setupAccount()
	require.NoError(t, err)
	genesisStateRoot := common.BytesToHash([]byte("foo"))
	addr, _, _, err := outgen.DeployAssertionChain(
		acc.txOpts,
		acc.backend,
		genesisStateRoot,
		big.NewInt(1), // 1 second challenge period.
	)
	require.NoError(t, err)

	acc.backend.Commit()

	chain, err := NewAssertionChain(
		ctx, addr, acc.txOpts, &bind.CallOpts{}, acc.accountAddr, acc.backend,
	)
	require.NoError(t, err)
	chalPeriod, err := chain.ChallengePeriodSeconds()
	require.NoError(t, err)
	require.Equal(t, time.Second, chalPeriod)
}

func TestCreateSuccessionChallenge_Fails(t *testing.T) {
	ctx := context.Background()
	acc, err := setupAccount()
	require.NoError(t, err)

	genesisStateRoot := common.BytesToHash([]byte("foo"))
	challengePeriodSeconds := big.NewInt(30)
	assertionChainAddr, _, _, err := outgen.DeployAssertionChain(
		acc.txOpts,
		acc.backend,
		genesisStateRoot,
		challengePeriodSeconds,
	)
	require.NoError(t, err)
	acc.backend.Commit()

	miniStakeValue := big.NewInt(1)
	chalManagerAddr, _, _, err := outgen.DeployChallengeManager(
		acc.txOpts,
		acc.backend,
		assertionChainAddr,
		miniStakeValue,
		challengePeriodSeconds,
		common.Address{}, // OSP entry contract.
	)
	require.NoError(t, err)
	acc.backend.Commit()

	chain, err := NewAssertionChain(
		ctx, assertionChainAddr, acc.txOpts, &bind.CallOpts{}, acc.accountAddr, acc.backend,
	)
	require.NoError(t, err)

	require.NoError(t, chain.UpdateChallengeManager(chalManagerAddr)) // What contract address?

	commit1 := util.StateCommitment{
		Height:    1,
		StateRoot: common.BytesToHash([]byte{1}),
	}
	genesisId := common.Hash{}

	err = chain.createAssertion(commit1, genesisId)
	require.NoError(t, err)
	acc.backend.Commit()

	commit2 := util.StateCommitment{
		Height:    1,
		StateRoot: common.BytesToHash([]byte{2}),
	}
	err = chain.createAssertion(commit2, genesisId)
	require.NoError(t, err)
	acc.backend.Commit()

	require.NoError(t, chain.CreateSuccessionChallenge(genesisId))
	err = chain.CreateSuccessionChallenge(common.BytesToHash([]byte("nyan")))
	require.NoError(t, err)
}

func TestCreateSuccessionChallenge(t *testing.T) {
	ctx := context.Background()
	acc, err := setupAccount()
	require.NoError(t, err)

	genesisStateRoot := common.BytesToHash([]byte("foo"))
	challengePeriodSeconds := big.NewInt(30)
	assertionChainAddr, _, _, err := outgen.DeployAssertionChain(
		acc.txOpts,
		acc.backend,
		genesisStateRoot,
		challengePeriodSeconds,
	)
	require.NoError(t, err)
	acc.backend.Commit()

	// Chain contract should be deployed.
	code, err := acc.backend.CodeAt(ctx, assertionChainAddr, nil)
	require.NoError(t, err)
	require.Equal(t, true, len(code) > 0)

	// miniStakeValue := big.NewInt(1)
	// chalManagerAddr, _, _, err := outgen.DeployChallengeManager(
	// 	acc.txOpts,
	// 	acc.backend,
	// 	assertionChainAddr,
	// 	miniStakeValue,
	// 	challengePeriodSeconds,
	// 	common.Address{}, // OSP entry contract.
	// )
	// require.NoError(t, err)
	// acc.backend.Commit()
	// _ = chalManagerAddr

	chain, err := NewAssertionChain(
		ctx, assertionChainAddr, acc.txOpts, &bind.CallOpts{}, acc.accountAddr, acc.backend,
	)
	require.NoError(t, err)

	genesisId := common.Hash{}

	t.Run("assertion does not exist", func(t *testing.T) {
		err = chain.CreateSuccessionChallenge([32]byte{9})
		require.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("assertion already rejected", func(t *testing.T) {
		t.Skip(
			"Needs a challenge manager to provide a winning claim first",
		)
	})
	t.Run("at least two children required", func(t *testing.T) {
		err = chain.CreateSuccessionChallenge(genesisId)
		require.ErrorIs(t, err, ErrInvalidChildren)

		commit1 := util.StateCommitment{
			Height:    1,
			StateRoot: common.BytesToHash([]byte{1}),
		}

		err = chain.createAssertion(commit1, genesisId)
		require.NoError(t, err)
		acc.backend.Commit()

		err = chain.CreateSuccessionChallenge(genesisId)
		require.ErrorIs(t, err, ErrInvalidChildren)
	})
	t.Run("too late to challenge", func(t *testing.T) {
		t.Skip("Advance the backend's time reference")
	})
	t.Run("OK", func(t *testing.T) {
		t.Skip("Advance the backend's time reference")
	})
	t.Run("challenge already exists", func(t *testing.T) {
		t.Skip("Create a fork and successful challenge first")
	})
}

// Represents a test EOA account in the simulated backend,
type testAccount struct {
	accountAddr common.Address
	backend     *backends.SimulatedBackend
	txOpts      *bind.TransactOpts
}

func setupAccount() (*testAccount, error) {
	genesis := make(core.GenesisAlloc)
	privKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	pubKeyECDSA, ok := privKey.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key to ECDSA")
	}

	// Strip off the 0x and the first 2 characters 04 which is always the
	// EC prefix and is not required.
	publicKeyBytes := crypto.FromECDSAPub(pubKeyECDSA)[4:]
	var pubKey = make([]byte, 48)
	copy(pubKey, publicKeyBytes)

	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	chainID := big.NewInt(1337)
	txOpts, err := bind.NewKeyedTransactorWithChainID(privKey, chainID)
	if err != nil {
		return nil, err
	}
	startingBalance, _ := new(big.Int).SetString(
		"100000000000000000000000000000000000000",
		10,
	)
	genesis[addr] = core.GenesisAccount{Balance: startingBalance}
	gasLimit := uint64(2100000000000)
	backend := backends.NewSimulatedBackend(genesis, gasLimit)
	return &testAccount{
		accountAddr: addr,
		backend:     backend,
		txOpts:      txOpts,
	}, nil
}
