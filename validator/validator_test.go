package validator

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"math/big"

	"github.com/OffchainLabs/challenge-protocol-v2/protocol"
	"github.com/OffchainLabs/challenge-protocol-v2/protocol/sol-implementation"
	"github.com/OffchainLabs/challenge-protocol-v2/state-manager"
	"github.com/OffchainLabs/challenge-protocol-v2/testing/mocks"
	"github.com/OffchainLabs/challenge-protocol-v2/util"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/backends"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"time"
)

func Test_onLeafCreation(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	t.Run("no fork detected", func(t *testing.T) {
		logsHook := test.NewGlobal()
		v, _, s := setupValidator(t)

		parentSeqNum := protocol.AssertionSequenceNumber(1)
		prevRoot := common.BytesToHash([]byte("foo"))
		seqNum := parentSeqNum + 1
		ev := &protocol.CreateLeafEvent{
			PrevSeqNum:    parentSeqNum,
			PrevStateHash: prevRoot,
			SeqNum:        seqNum,
			StateHash:     common.BytesToHash([]byte("bar")),
			Validator:     common.BytesToAddress([]byte("alice")),
		}

		s.On("HasStateCommitment", ctx, util.StateCommitment{}).Return(false)

		err := v.onLeafCreated(ctx, ev)
		require.NoError(t, err)
		AssertLogsContain(t, logsHook, "New leaf appended")
		AssertLogsContain(t, logsHook, "No fork detected in assertion tree")
	})
	t.Run("fork leads validator to challenge leaf", func(t *testing.T) {
		logsHook := test.NewGlobal()
		ctx := context.Background()
		createdData := createTwoValidatorFork(t, ctx)

		// Setup our mock state manager to agree on leaf1 but disagree on leaf2.
		manager := &mocks.MockStateManager{}
		manager.On("HasStateCommitment", ctx, util.StateCommitment{
			Height:    createdData.leaf1.Height,
			StateRoot: createdData.leaf1.StateHash,
		}).Return(true)
		manager.On("HasStateCommitment", ctx, util.StateCommitment{
			Height:    createdData.leaf2.Height,
			StateRoot: createdData.leaf2.StateHash,
		}).Return(false)

		manager.On(
			"HistoryCommitmentUpTo",
			ctx,
			uint64(createdData.leaf1.Height),
		).Return(util.HistoryCommitment{
			Height: createdData.leaf1.Height,
			Merkle: createdData.leaf1.StateHash, // TODO: Change
		}, nil)

		validator, err := New(ctx, createdData.assertionChains[1], manager)
		require.NoError(t, err)

		err = validator.onLeafCreated(ctx, createdData.leaf1)
		require.NoError(t, err)
		err = validator.onLeafCreated(ctx, createdData.leaf2)
		require.NoError(t, err)

		AssertLogsContain(t, logsHook, "New leaf appended")
		AssertLogsContain(t, logsHook, "Successfully created challenge and added leaf")

		err = validator.onLeafCreated(ctx, createdData.leaf2)
		require.ErrorContains(t, err, "Vertex already exists")
	})
}

func Test_onChallengeStarted(t *testing.T) {
	ctx := context.Background()
	logsHook := test.NewGlobal()

	createdData := createTwoValidatorFork(t, ctx)

	manager := &mocks.MockStateManager{}
	manager.On("HasStateCommitment", ctx, util.StateCommitment{
		Height:    createdData.leaf1.Height,
		StateRoot: createdData.leaf1.StateHash,
	}).Return(false)
	manager.On("HasStateCommitment", ctx, util.StateCommitment{
		Height:    createdData.leaf2.Height,
		StateRoot: createdData.leaf2.StateHash,
	}).Return(true)

	manager.On(
		"HistoryCommitmentUpTo",
		ctx,
		createdData.leaf1.Height,
	).Return(util.HistoryCommitment{
		Height: createdData.leaf1.Height,
		Merkle: createdData.leaf1.StateHash,
	}, nil)

	manager.On(
		"HistoryCommitmentUpTo",
		ctx,
		createdData.leaf2.Height,
	).Return(util.HistoryCommitment{
		Height: createdData.leaf2.Height,
		Merkle: createdData.leaf2.StateHash,
	}, nil)

	validator, err := New(ctx, createdData.assertionChains[1], manager, WithChallengeVertexWakeInterval(time.Hour))
	require.NoError(t, err)

	err = validator.onLeafCreated(ctx, createdData.leaf1)
	require.NoError(t, err)
	err = validator.onLeafCreated(ctx, createdData.leaf2)
	require.NoError(t, err)
	AssertLogsContain(t, logsHook, "New leaf appended")
	AssertLogsContain(t, logsHook, "New leaf appended")
	AssertLogsContain(t, logsHook, "Successfully created challenge and added leaf")

	var challenge util.Option[protocol.Challenge]
	err = validator.chain.Call(func(tx protocol.ActiveTx) error {
		genesisId, err := validator.chain.GetAssertionId(ctx, tx, 0)
		require.NoError(t, err)

		manager, err := validator.chain.CurrentChallengeManager(ctx, tx)
		require.NoError(t, err)

		chalId, err := manager.CalculateChallengeHash(ctx, tx, common.Hash(genesisId), protocol.BlockChallenge)
		require.NoError(t, err)

		challenge, err = manager.GetChallenge(ctx, tx, chalId)
		require.NoError(t, err)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, false, challenge.IsNone())

	manager = &mocks.MockStateManager{}
	manager.On("HasStateCommitment", ctx, util.StateCommitment{
		Height:    createdData.leaf1.Height,
		StateRoot: createdData.leaf1.StateHash,
	}).Return(false)
	manager.On("HasStateCommitment", ctx, util.StateCommitment{
		Height:    createdData.leaf2.Height,
		StateRoot: createdData.leaf2.StateHash,
	}).Return(true)

	forked1 := common.BytesToHash([]byte("forked commit"))
	forked2 := common.BytesToHash([]byte("forked commit"))
	manager.On("HistoryCommitmentUpTo", ctx, createdData.leaf1.Height).Return(util.HistoryCommitment{
		Height: createdData.leaf1.Height,
		Merkle: forked1,
	}, nil)
	manager.On("HistoryCommitmentUpTo", ctx, createdData.leaf2.Height).Return(util.HistoryCommitment{
		Height: createdData.leaf2.Height,
		Merkle: forked2,
	}, nil)
	validator.stateManager = manager

	parentStateCommitment, err := challenge.Unwrap().ParentStateCommitment(ctx, &mocks.MockActiveTx{ReadWriteTx: false})
	require.NoError(t, err)
	err = validator.onChallengeStarted(ctx, &protocol.StartChallengeEvent{
		ParentSeqNum:    0,
		ParentStateHash: parentStateCommitment.StateRoot,
		ParentStaker:    common.Address{},
		Validator:       common.BytesToAddress([]byte("other validator")),
	})
	require.NoError(t, err)
	AssertLogsContain(t, logsHook, "Received challenge for a created leaf, added own leaf")

	err = validator.onChallengeStarted(ctx, &protocol.StartChallengeEvent{
		ParentSeqNum:    0,
		ParentStateHash: parentStateCommitment.StateRoot,
		ParentStaker:    common.Address{},
		Validator:       common.BytesToAddress([]byte("other validator")),
	})
	require.ErrorContains(t, err, "Vertex already exists")
}

func Test_submitAndFetchProtocolChallenge(t *testing.T) {
	ctx := context.Background()
	createdData := createTwoValidatorFork(t, ctx)

	var genesis protocol.Assertion
	err := createdData.assertionChains[1].Call(func(tx protocol.ActiveTx) error {
		conf, err := createdData.assertionChains[1].LatestConfirmed(ctx, tx)
		if err != nil {
			return err
		}
		genesis = conf
		return nil
	})
	require.NoError(t, err)

	// Setup our mock state manager to agree on leaf1 but disagree on leaf2.
	manager := &mocks.MockStateManager{}
	validator, err := New(ctx, createdData.assertionChains[1], manager)
	require.NoError(t, err)

	wantedChallenge, err := validator.submitProtocolChallenge(ctx, genesis.SeqNum())
	require.NoError(t, err)
	gotChallenge, err := validator.fetchProtocolChallenge(ctx, genesis.SeqNum())
	require.NoError(t, err)
	require.Equal(t, wantedChallenge, gotChallenge)
}

type createdValidatorFork struct {
	leaf1           *protocol.CreateLeafEvent
	leaf2           *protocol.CreateLeafEvent
	assertionChains []*solimpl.AssertionChain
	accounts        []*testAccount
	backend         *backends.SimulatedBackend
	stateRoots      []common.Hash
}

func createTwoValidatorFork(
	t *testing.T,
	ctx context.Context,
) *createdValidatorFork {
	chains, accs, _, backend := setupAssertionChains(t, 3)
	prevInboxMaxCount := big.NewInt(1)

	var genesis protocol.Assertion
	var assertion protocol.Assertion
	var forkedAssertion protocol.Assertion
	err := chains[1].Call(func(tx protocol.ActiveTx) error {
		genesisAssertion, err := chains[1].AssertionBySequenceNum(ctx, tx, 0)
		if err != nil {
			return err
		}
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
	genesisStateHash := protocol.ComputeStateHash(genesisState, big.NewInt(1))

	require.Equal(t, genesisStateHash, genesis.StateHash(), "Genesis state hash unequal")

	height := uint64(0)
	stateRoots := make([]common.Hash, 0)
	stateRoots = append(stateRoots, genesisStateHash)

	latestBlockHash := common.Hash{}
	for i := 1; i < 100; i++ {
		height += 1
		latestBlockHash = backend.Commit()
		state := &protocol.ExecutionState{
			GlobalState: protocol.GoGlobalState{
				BlockHash: latestBlockHash,
				Batch:     1,
			},
			MachineStatus: protocol.MachineStatusFinished,
		}
		stateRoots = append(stateRoots, protocol.ComputeStateHash(state, big.NewInt(1)))
	}

	err = chains[1].Tx(func(tx protocol.ActiveTx) error {
		assertion, err = chains[1].CreateAssertion(
			ctx,
			tx,
			height, // Height.
			genesis.SeqNum(),
			genesisState,
			&protocol.ExecutionState{
				GlobalState: protocol.GoGlobalState{
					BlockHash: latestBlockHash,
					Batch:     1,
				},
				MachineStatus: protocol.MachineStatusFinished,
			},
			prevInboxMaxCount,
		)
		if err != nil {
			return err
		}
		return nil
	})
	require.NoError(t, err)

	err = chains[2].Tx(func(tx protocol.ActiveTx) error {
		forkedAssertion, err = chains[2].CreateAssertion(
			ctx,
			tx,
			height+1, // Height.
			genesis.SeqNum(),
			genesisState,
			&protocol.ExecutionState{
				GlobalState: protocol.GoGlobalState{
					BlockHash: common.BytesToHash([]byte("malicious commit")),
					Batch:     1,
				},
				MachineStatus: protocol.MachineStatusFinished,
			},
			prevInboxMaxCount,
		)
		if err != nil {
			return err
		}
		return nil
	})
	require.NoError(t, err)

	ev1 := &protocol.CreateLeafEvent{
		PrevSeqNum:    genesis.PrevSeqNum(),
		PrevStateHash: genesis.StateHash(),
		PrevHeight:    0,
		Height:        assertion.Height(),
		SeqNum:        assertion.SeqNum(),
		StateHash:     assertion.StateHash(),
		Validator:     accs[1].accountAddr,
	}
	ev2 := &protocol.CreateLeafEvent{
		PrevSeqNum:    genesis.PrevSeqNum(),
		PrevStateHash: genesis.StateHash(),
		PrevHeight:    0,
		Height:        forkedAssertion.Height(),
		SeqNum:        forkedAssertion.SeqNum(),
		StateHash:     forkedAssertion.StateHash(),
		Validator:     accs[2].accountAddr,
	}
	return &createdValidatorFork{
		leaf1:           ev1,
		leaf2:           ev2,
		assertionChains: chains,
		accounts:        accs,
		backend:         backend,
		stateRoots:      stateRoots,
	}
}

func Test_findLatestValidAssertion(t *testing.T) {
	ctx := context.Background()
	tx := &mocks.MockActiveTx{}
	t.Run("only valid latest assertion is genesis", func(t *testing.T) {
		v, p, _ := setupValidator(t)
		genesis := &mocks.MockAssertion{
			MockSeqNum:    0,
			MockHeight:    0,
			MockStateHash: common.Hash{},
			Prev:          util.None[*mocks.MockAssertion](),
		}
		p.On("LatestConfirmed", ctx, tx).Return(genesis, nil)
		p.On("NumAssertions", ctx, tx).Return(uint64(100), nil)
		latestValid, err := v.findLatestValidAssertion(ctx)
		require.NoError(t, err)
		require.Equal(t, genesis.SeqNum(), latestValid)
	})
	t.Run("all are valid, latest one is picked", func(t *testing.T) {
		v, p, s := setupValidator(t)
		assertions := setupAssertions(10)
		for _, a := range assertions {
			v.assertions[a.SeqNum()] = &protocol.CreateLeafEvent{
				StateHash: a.StateHash(),
				Height:    a.Height(),
				SeqNum:    a.SeqNum(),
			}
			s.On("HasStateCommitment", ctx, util.StateCommitment{
				Height:    a.Height(),
				StateRoot: a.StateHash(),
			}).Return(true)
		}
		p.On("LatestConfirmed", ctx, tx).Return(assertions[0], nil)
		p.On("NumAssertions", ctx, tx).Return(uint64(len(assertions)), nil)

		latestValid, err := v.findLatestValidAssertion(ctx)
		require.NoError(t, err)
		require.Equal(t, assertions[len(assertions)-1].SeqNum(), latestValid)
	})
	t.Run("latest valid is behind", func(t *testing.T) {
		v, p, s := setupValidator(t)
		assertions := setupAssertions(10)
		for i, a := range assertions {
			v.assertions[a.SeqNum()] = &protocol.CreateLeafEvent{
				StateHash: a.StateHash(),
				Height:    a.Height(),
				SeqNum:    a.SeqNum(),
			}
			if i <= 5 {
				s.On("HasStateCommitment", ctx, util.StateCommitment{
					Height:    a.Height(),
					StateRoot: a.StateHash(),
				}).Return(true)
			} else {
				s.On("HasStateCommitment", ctx, util.StateCommitment{
					Height:    a.Height(),
					StateRoot: a.StateHash(),
				}).Return(false)
			}
		}
		p.On("LatestConfirmed", ctx, tx).Return(assertions[0], nil)
		p.On("NumAssertions", ctx, tx).Return(uint64(len(assertions)), nil)
		latestValid, err := v.findLatestValidAssertion(ctx)
		require.NoError(t, err)
		require.Equal(t, assertions[5].SeqNum(), latestValid)
	})
}

func setupAssertions(num int) []protocol.Assertion {
	if num == 0 {
		return make([]protocol.Assertion, 0)
	}
	genesis := &mocks.MockAssertion{
		MockSeqNum:    0,
		MockHeight:    0,
		MockStateHash: common.Hash{},
		Prev:          util.None[*mocks.MockAssertion](),
	}
	assertions := []protocol.Assertion{genesis}
	for i := 1; i < num; i++ {
		assertions = append(assertions, protocol.Assertion(&mocks.MockAssertion{
			MockSeqNum:    protocol.AssertionSequenceNumber(i),
			MockHeight:    uint64(i),
			MockStateHash: common.BytesToHash([]byte(fmt.Sprintf("%d", i))),
			Prev:          util.Some(assertions[i-1].(*mocks.MockAssertion)),
		}))
	}
	return assertions
}

func setupValidatorWithChain(
	t testing.TB, chain protocol.Protocol, manager statemanager.Manager, staker common.Address,
) *Validator {
	v, err := New(context.Background(), chain, manager, WithAddress(staker))
	require.NoError(t, err)
	return v
}

func setupValidator(t testing.TB) (*Validator, *mocks.MockProtocol, *mocks.MockStateManager) {
	p := &mocks.MockProtocol{}
	s := &mocks.MockStateManager{}
	v, err := New(context.Background(), p, s)
	require.NoError(t, err)
	return v, p, s
}

// AssertLogsContain checks that the desired string is a subset of the current log output.
func AssertLogsContain(tb testing.TB, hook *test.Hook, want string, msg ...interface{}) {
	checkLogs(tb, hook, want, true, msg...)
}

// AssertLogsDoNotContain is the inverse check of LogsContain.
func AssertLogsDoNotContain(tb testing.TB, hook *test.Hook, want string, msg ...interface{}) {
	checkLogs(tb, hook, want, false, msg...)
}

// LogsContain checks whether a given substring is a part of logs. If flag=false, inverse is checked.
func checkLogs(tb testing.TB, hook *test.Hook, want string, flag bool, msg ...interface{}) {
	_, file, line, _ := runtime.Caller(2)
	entries := hook.AllEntries()
	logs := make([]string, 0, len(entries))
	match := false
	for _, e := range entries {
		msg, err := e.String()
		if err != nil {
			tb.Errorf("%s:%d Failed to format log entry to string: %v", filepath.Base(file), line, err)
			return
		}
		if strings.Contains(msg, want) {
			match = true
		}
		for _, field := range e.Data {
			fieldStr, ok := field.(string)
			if !ok {
				continue
			}
			if strings.Contains(fieldStr, want) {
				match = true
			}
		}
		logs = append(logs, msg)
	}
	var errMsg string
	if flag && !match {
		errMsg = parseMsg("Expected log not found", msg...)
	} else if !flag && match {
		errMsg = parseMsg("Unexpected log found", msg...)
	}
	if errMsg != "" {
		tb.Errorf("%s:%d %s: %v\nSearched logs:\n%v", filepath.Base(file), line, errMsg, want, logs)
	}
}

func parseMsg(defaultMsg string, msg ...interface{}) string {
	if len(msg) >= 1 {
		msgFormat, ok := msg[0].(string)
		if !ok {
			return defaultMsg
		}
		return fmt.Sprintf(msgFormat, msg[1:]...)
	}
	return defaultMsg
}
