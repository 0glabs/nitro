package arbtest

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/OffchainLabs/bold/assertions"
	protocol "github.com/OffchainLabs/bold/chain-abstraction"
	solimpl "github.com/OffchainLabs/bold/chain-abstraction/sol-implementation"
	challengemanager "github.com/OffchainLabs/bold/challenge-manager"
	modes "github.com/OffchainLabs/bold/challenge-manager/types"
	l2stateprovider "github.com/OffchainLabs/bold/layer2-state-provider"
	"github.com/OffchainLabs/bold/solgen/go/bridgegen"
	"github.com/OffchainLabs/bold/solgen/go/mocksgen"
	"github.com/OffchainLabs/bold/solgen/go/rollupgen"
	challenge_testing "github.com/OffchainLabs/bold/testing"
	"github.com/OffchainLabs/bold/testing/setup"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/arbcompress"
	"github.com/offchainlabs/nitro/arbnode"
	"github.com/offchainlabs/nitro/arbnode/execution"
	"github.com/offchainlabs/nitro/arbos/l2pricing"
	"github.com/offchainlabs/nitro/cmd/chaininfo"
	"github.com/offchainlabs/nitro/staker"
	"github.com/offchainlabs/nitro/statetransfer"
	"github.com/offchainlabs/nitro/util"
	"github.com/offchainlabs/nitro/util/signature"
	"github.com/offchainlabs/nitro/validator/server_common"
	"github.com/offchainlabs/nitro/validator/valnode"
)

// One Arbitrum block had 1,849,212,947 total opcodes. The closest, higher power of two
// is 2^31. So we if we make our small step heights 2^20, we need 2048 big steps
// to cover the block. With 2^20, our small step history commitments will be approx
// 32 Mb of state roots in memory at once.
var (
	blockChallengeLeafHeight     = uint64(1 << 5) // 32
	bigStepChallengeLeafHeight   = uint64(1 << 5) // testing 5 big step levels, 2^5 each, with small step equalting to 2^31 total.
	smallStepChallengeLeafHeight = uint64(1 << 6)
)

func TestBoldProtocol(t *testing.T) {
	t.Parallel()
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	var transferGas = util.NormalizeL2GasForL1GasInitial(800_000, params.GWei) // include room for aggregator L1 costs
	l2chainConfig := params.ArbitrumDevTestChainConfig()
	l2info := NewBlockChainTestInfo(
		t,
		types.NewArbitrumSigner(types.NewLondonSigner(l2chainConfig.ChainID)), big.NewInt(l2pricing.InitialBaseFeeWei*2),
		transferGas,
	)
	ownerBal := big.NewInt(params.Ether)
	ownerBal.Mul(ownerBal, big.NewInt(1_000_000))
	l2info.GenerateGenesisAccount("Owner", ownerBal)

	_, l2nodeA, _, _, l1info, _, l1client, l1stack, assertionChain, stakeTokenAddr := createTestNodeOnL1ForBoldProtocol(t, ctx, true, nil, l2chainConfig, nil, l2info)
	defer requireClose(t, l1stack)
	defer l2nodeA.StopAndWait()

	_, l2nodeB, assertionChainB := create2ndNodeWithConfigForBoldProtocol(t, ctx, l2nodeA, l1stack, l1info, &l2info.ArbInitData, arbnode.ConfigDefaultL1Test(), nil, stakeTokenAddr)
	defer l2nodeB.StopAndWait()

	nodeAGenesis := l2nodeA.Execution.Backend.APIBackend().CurrentHeader().Hash()
	nodeBGenesis := l2nodeB.Execution.Backend.APIBackend().CurrentHeader().Hash()
	if nodeAGenesis != nodeBGenesis {
		Fail(t, "node A L2 genesis hash", nodeAGenesis, "!= node B L2 genesis hash", nodeBGenesis)
	}

	deployAuth := l1info.GetDefaultTransactOpts("RollupOwner", ctx)

	balance := big.NewInt(params.Ether)
	balance.Mul(balance, big.NewInt(100))
	TransferBalance(t, "Faucet", "Asserter", balance, l1info, l1client, ctx)
	TransferBalance(t, "Faucet", "EvilAsserter", balance, l1info, l1client, ctx)
	l1authB := l1info.GetDefaultTransactOpts("EvilAsserter", ctx)

	t.Log("Setting the minimum assertion period")
	rollup, err := rollupgen.NewRollupAdminLogicTransactor(assertionChain.RollupAddress(), l1client)
	Require(t, err)
	tx, err := rollup.SetMinimumAssertionPeriod(&deployAuth, big.NewInt(0))
	Require(t, err)
	_, err = EnsureTxSucceeded(ctx, l1client, tx)
	Require(t, err)
	rollup, err = rollupgen.NewRollupAdminLogicTransactor(assertionChainB.RollupAddress(), l1client)
	Require(t, err)
	tx, err = rollup.SetMinimumAssertionPeriod(&deployAuth, big.NewInt(0))
	Require(t, err)
	_, err = EnsureTxSucceeded(ctx, l1client, tx)
	Require(t, err)

	valConfig := staker.L1ValidatorConfig{}
	valConfig.Strategy = "MakeNodes"
	_, valStack := createTestValidationNode(t, ctx, &valnode.TestValidationConfig)
	blockValidatorConfig := staker.TestBlockValidatorConfig

	statelessA, err := staker.NewStatelessBlockValidator(
		l2nodeA.InboxReader,
		l2nodeA.InboxTracker,
		l2nodeA.TxStreamer,
		l2nodeA.Execution.Recorder,
		l2nodeA.ArbDB,
		nil,
		StaticFetcherFrom(t, &blockValidatorConfig),
		valStack,
	)
	Require(t, err)
	err = statelessA.Start(ctx)
	Require(t, err)

	statelessB, err := staker.NewStatelessBlockValidator(
		l2nodeB.InboxReader,
		l2nodeB.InboxTracker,
		l2nodeB.TxStreamer,
		l2nodeB.Execution.Recorder,
		l2nodeB.ArbDB,
		nil,
		StaticFetcherFrom(t, &blockValidatorConfig),
		valStack,
	)
	Require(t, err)
	err = statelessB.Start(ctx)
	Require(t, err)

	stateManager, err := staker.NewStateManager(
		statelessA,
		"/tmp/good",
		[]l2stateprovider.Height{
			l2stateprovider.Height(blockChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(smallStepChallengeLeafHeight),
		},
		"good",
	)
	Require(t, err)

	poster := assertions.NewPoster(
		assertionChain,
		stateManager,
		"good",
		time.Hour,
	)

	stateManagerB, err := staker.NewStateManager(
		statelessB,
		"/tmp/evil",
		[]l2stateprovider.Height{
			l2stateprovider.Height(blockChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(smallStepChallengeLeafHeight),
		},
		"evil",
	)
	Require(t, err)

	chainB, err := solimpl.NewAssertionChain(
		ctx,
		assertionChain.RollupAddress(),
		&l1authB,
		l1client,
	)
	Require(t, err)
	posterB := assertions.NewPoster(
		chainB,
		stateManagerB,
		"evil",
		time.Hour,
	)

	l2info.GenerateAccount("Destination")
	sequencerTxOpts := l1info.GetDefaultTransactOpts("Sequencer", ctx)

	honestSeqInbox := l1info.GetAddress("SequencerInbox")
	evilSeqInbox := l1info.GetAddress("EvilSequencerInbox")
	honestSeqInboxBinding, err := bridgegen.NewSequencerInbox(honestSeqInbox, l1client)
	Require(t, err)
	evilSeqInboxBinding, err := bridgegen.NewSequencerInbox(evilSeqInbox, l1client)
	Require(t, err)

	// Post batches to the honest and evil sequencer inbox that are internally equal.
	// This means the honest and evil sequencer inboxes will agree with all messages in the batch.
	totalMessagesPosted := int64(0)
	numMessagesPerBatch := int64(5)
	divergeAt := int64(-1)
	makeBoldBatch(t, l2nodeA, l2info, l1client, &sequencerTxOpts, honestSeqInboxBinding, honestSeqInbox, numMessagesPerBatch, divergeAt)
	l2info.Accounts["Owner"].Nonce = 0
	makeBoldBatch(t, l2nodeB, l2info, l1client, &sequencerTxOpts, evilSeqInboxBinding, evilSeqInbox, numMessagesPerBatch, divergeAt)
	totalMessagesPosted += numMessagesPerBatch

	// Next, we post another batch, this time containing more messages.
	// We diverge at message index 5 within the evil node's batch.
	l2info.Accounts["Owner"].Nonce = 5
	numMessagesPerBatch = int64(10)
	makeBoldBatch(t, l2nodeA, l2info, l1client, &sequencerTxOpts, honestSeqInboxBinding, honestSeqInbox, numMessagesPerBatch, divergeAt)
	l2info.Accounts["Owner"].Nonce = 5
	divergeAt = int64(5)
	makeBoldBatch(t, l2nodeB, l2info, l1client, &sequencerTxOpts, evilSeqInboxBinding, evilSeqInbox, numMessagesPerBatch, divergeAt)
	totalMessagesPosted += numMessagesPerBatch

	bcA, err := l2nodeA.InboxTracker.GetBatchCount()
	Require(t, err)
	bcB, err := l2nodeB.InboxTracker.GetBatchCount()
	Require(t, err)
	msgA, err := l2nodeA.InboxTracker.GetBatchMessageCount(bcA - 1)
	Require(t, err)
	msgB, err := l2nodeB.InboxTracker.GetBatchMessageCount(bcB - 1)
	Require(t, err)

	t.Logf("Node A batch count %d, msgs %d", bcA, msgA)
	t.Logf("Node B batch count %d, msgs %d", bcB, msgB)

	for {
		nodeALatest := l2nodeA.Execution.Backend.APIBackend().CurrentHeader()
		nodeBLatest := l2nodeB.Execution.Backend.APIBackend().CurrentHeader()
		isCaughtUp := nodeALatest.Number.Uint64() == uint64(totalMessagesPosted)
		areEqual := nodeALatest.Number.Uint64() == nodeBLatest.Number.Uint64()
		if isCaughtUp && areEqual {
			if nodeALatest.Hash() == nodeBLatest.Hash() {
				Fatal(t, "node A L2 hash", nodeALatest, "matches node B L2 hash", nodeBLatest)
			}
			break
		}
	}

	goodBridge, err := bridgegen.NewSequencerInbox(honestSeqInbox, l1client)
	Require(t, err)
	totalGoodBatches, err := goodBridge.BatchCount(&bind.CallOpts{})
	Require(t, err)

	evilBridge, err := bridgegen.NewSequencerInbox(evilSeqInbox, l1client)
	Require(t, err)
	totalEvilBatches, err := evilBridge.BatchCount(&bind.CallOpts{})
	Require(t, err)

	t.Logf("Total good %d, total bad %d", totalGoodBatches.Uint64(), totalEvilBatches.Uint64())

	t.Log("Honest party posting assertion at batch 1, pos 0")
	_, err = poster.PostAssertion(ctx)
	Require(t, err)

	t.Log("Honest party posting assertion at batch 3, pos 0")
	_, err = poster.PostAssertion(ctx)
	Require(t, err)

	t.Log("Evil party posting assertion at batch 3, pos 0")
	_, err = posterB.PostAssertion(ctx)
	Require(t, err)

	provider := l2stateprovider.NewHistoryCommitmentProvider(
		stateManager,
		stateManager,
		stateManager,
		[]l2stateprovider.Height{
			l2stateprovider.Height(blockChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(smallStepChallengeLeafHeight),
		},
		stateManager,
	)

	evilProvider := l2stateprovider.NewHistoryCommitmentProvider(
		stateManagerB,
		stateManagerB,
		stateManagerB,
		[]l2stateprovider.Height{
			l2stateprovider.Height(blockChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(bigStepChallengeLeafHeight),
			l2stateprovider.Height(smallStepChallengeLeafHeight),
		},
		stateManagerB,
	)

	manager, err := challengemanager.New(
		ctx,
		assertionChain,
		l1client,
		provider,
		assertionChain.RollupAddress(),
		challengemanager.WithName("honest"),
		challengemanager.WithMode(modes.DefensiveMode),
		challengemanager.WithAssertionPostingInterval(time.Hour),
		challengemanager.WithAssertionScanningInterval(time.Hour),
		challengemanager.WithEdgeTrackerWakeInterval(time.Second),
	)
	Require(t, err)
	manager.Start(ctx)
	managerB, err := challengemanager.New(
		ctx,
		chainB,
		l1client,
		evilProvider,
		assertionChain.RollupAddress(),
		challengemanager.WithName("evil"),
		challengemanager.WithMode(modes.DefensiveMode),
		challengemanager.WithAssertionPostingInterval(time.Hour),
		challengemanager.WithAssertionScanningInterval(time.Hour),
		challengemanager.WithEdgeTrackerWakeInterval(time.Second),
	)
	Require(t, err)
	managerB.Start(ctx)

	// Every 10 seconds, send an L1 transaction.
	// delay := time.Second * 10
	// for {
	// 	time.Sleep(delay)
	// 	balance := big.NewInt(params.GWei)
	// 	TransferBalance(t, "Faucet", "Asserter", balance, l1info, l1client, ctx)
	// 	latestBlock, err := l1client.BlockNumber(ctx)
	// 	Require(t, err)
	// 	if latestBlock > 200 {
	// 		delay = time.Second
	// 	}
	// }
}

func createTestNodeOnL1ForBoldProtocol(
	t *testing.T,
	ctx context.Context,
	isSequencer bool,
	nodeConfig *arbnode.Config,
	chainConfig *params.ChainConfig,
	stackConfig *node.Config,
	l2info_in info,
) (
	l2info info, currentNode *arbnode.Node, l2client *ethclient.Client, l2stack *node.Node,
	l1info info, l1backend *eth.Ethereum, l1client *ethclient.Client, l1stack *node.Node,
	assertionChain *solimpl.AssertionChain, stakeTokenAddr common.Address,
) {
	if nodeConfig == nil {
		nodeConfig = arbnode.ConfigDefaultL1Test()
		nodeConfig.ParentChainReader.OldHeaderTimeout = time.Minute * 10
	}
	if chainConfig == nil {
		chainConfig = params.ArbitrumDevTestChainConfig()
	}
	nodeConfig.BatchPoster.DataPoster.MaxMempoolTransactions = 0
	fatalErrChan := make(chan error, 10)
	l1info, l1client, l1backend, l1stack = createTestL1BlockChain(t, nil)
	var l2chainDb ethdb.Database
	var l2arbDb ethdb.Database
	var l2blockchain *core.BlockChain
	l2info = l2info_in
	if l2info == nil {
		l2info = NewArbTestInfo(t, chainConfig.ChainID)
	}

	l1info.GenerateAccount("RollupOwner")
	l1info.GenerateAccount("Sequencer")
	l1info.GenerateAccount("User")
	l1info.GenerateAccount("Asserter")
	l1info.GenerateAccount("EvilAsserter")

	SendWaitTestTransactions(t, ctx, l1client, []*types.Transaction{
		l1info.PrepareTx("Faucet", "RollupOwner", 30000, big.NewInt(9223372036854775807), nil),
		l1info.PrepareTx("Faucet", "Sequencer", 30000, big.NewInt(9223372036854775807), nil),
		l1info.PrepareTx("Faucet", "User", 30000, big.NewInt(9223372036854775807), nil),
		l1info.PrepareTx("Faucet", "Asserter", 30000, big.NewInt(9223372036854775807), nil),
		l1info.PrepareTx("Faucet", "EvilAsserter", 30000, big.NewInt(9223372036854775807), nil),
	})

	l1TransactionOpts := l1info.GetDefaultTransactOpts("RollupOwner", ctx)
	stakeToken, tx, tokenBindings, err := mocksgen.DeployTestWETH9(
		&l1TransactionOpts,
		l1client,
		"Weth",
		"WETH",
	)
	Require(t, err)
	EnsureTxSucceeded(ctx, l1client, tx)
	stakeTokenAddr = stakeToken
	value, ok := new(big.Int).SetString("10000", 10)
	if !ok {
		t.Fatal(t, "could not set value")
	}
	l1TransactionOpts.Value = value
	tx, err = tokenBindings.Deposit(&l1TransactionOpts)
	Require(t, err)
	EnsureTxSucceeded(ctx, l1client, tx)
	l1TransactionOpts.Value = nil

	addresses, assertionChainBindings := deployContractsOnly(t, ctx, l1info, l1client, chainConfig.ChainID, stakeToken)

	l1info.SetContract("Bridge", addresses.Bridge)
	l1info.SetContract("SequencerInbox", addresses.SequencerInbox)
	l1info.SetContract("Inbox", addresses.Inbox)

	_, l2stack, l2chainDb, l2arbDb, l2blockchain = createL2BlockChainWithStackConfig(t, l2info, "", chainConfig, getInitMessage(ctx, t, l1client, addresses), stackConfig, nil)
	assertionChain = assertionChainBindings
	var sequencerTxOptsPtr *bind.TransactOpts
	var dataSigner signature.DataSignerFunc
	if isSequencer {
		sequencerTxOpts := l1info.GetDefaultTransactOpts("Sequencer", ctx)
		sequencerTxOptsPtr = &sequencerTxOpts
		dataSigner = signature.DataSignerFromPrivateKey(l1info.GetInfoWithPrivKey("Sequencer").PrivateKey)
	}

	if !isSequencer {
		nodeConfig.BatchPoster.Enable = false
		nodeConfig.DelayedSequencer.Enable = false
	}

	AddDefaultValNode(t, ctx, nodeConfig, true)

	currentNode, err = arbnode.CreateNode(
		ctx, l2stack, l2chainDb, l2arbDb, NewFetcherFromConfig(nodeConfig), l2blockchain, l1client,
		addresses, sequencerTxOptsPtr, sequencerTxOptsPtr, dataSigner, fatalErrChan,
	)
	Require(t, err)

	Require(t, currentNode.Start(ctx))

	l2client = ClientForStack(t, l2stack)

	StartWatchChanErr(t, ctx, fatalErrChan, currentNode)

	return
}

func deployContractsOnly(
	t *testing.T,
	ctx context.Context,
	l1info info,
	backend *ethclient.Client,
	chainId *big.Int,
	stakeToken common.Address,
) (*chaininfo.RollupAddresses, *solimpl.AssertionChain) {
	l1TransactionOpts := l1info.GetDefaultTransactOpts("RollupOwner", ctx)
	locator, err := server_common.NewMachineLocator("")
	Require(t, err)
	wasmModuleRoot := locator.LatestWasmModuleRoot()

	prod := false
	loserStakeEscrow := common.Address{}
	miniStake := big.NewInt(1)
	genesisExecutionState := rollupgen.ExecutionState{
		GlobalState:   rollupgen.GlobalState{},
		MachineStatus: 1,
	}
	genesisInboxCount := big.NewInt(0)
	anyTrustFastConfirmer := common.Address{}
	cfg := challenge_testing.GenerateRollupConfig(
		prod,
		wasmModuleRoot,
		l1TransactionOpts.From,
		chainId,
		loserStakeEscrow,
		miniStake,
		stakeToken,
		genesisExecutionState,
		genesisInboxCount,
		anyTrustFastConfirmer,
		challenge_testing.WithLayerZeroHeights(&protocol.LayerZeroHeights{
			BlockChallengeHeight:     blockChallengeLeafHeight,
			BigStepChallengeHeight:   bigStepChallengeLeafHeight,
			SmallStepChallengeHeight: smallStepChallengeLeafHeight,
		}),
		challenge_testing.WithNumBigStepLevels(uint8(5)),       // TODO: Hardcoded.
		challenge_testing.WithConfirmPeriodBlocks(uint64(150)), // TODO: Hardcoded.
	)
	config, err := json.Marshal(params.ArbitrumDevTestChainConfig())
	Require(t, err)
	cfg.ChainConfig = string(config)
	addresses, err := setup.DeployFullRollupStack(
		ctx,
		backend,
		&l1TransactionOpts,
		l1info.GetAddress("Sequencer"),
		cfg,
		false, // do not use mock bridge.
		false, // do not use a mock one step prover
	)
	Require(t, err)

	asserter := l1info.GetDefaultTransactOpts("Asserter", ctx)
	evilAsserter := l1info.GetDefaultTransactOpts("EvilAsserter", ctx)
	chain, err := solimpl.NewAssertionChain(
		ctx,
		addresses.Rollup,
		&asserter,
		backend,
	)
	Require(t, err)

	chalManager, err := chain.SpecChallengeManager(ctx)
	Require(t, err)
	chalManagerAddr := chalManager.Address()
	seed, ok := new(big.Int).SetString("1000", 10)
	if !ok {
		t.Fatal("not ok")
	}
	value, ok := new(big.Int).SetString("10000", 10)
	if !ok {
		t.Fatal(t, "could not set value")
	}
	tokenBindings, err := mocksgen.NewTestWETH9(stakeToken, backend)
	Require(t, err)
	tx, err := tokenBindings.TestWETH9Transactor.Transfer(&l1TransactionOpts, asserter.From, seed)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)
	tx, err = tokenBindings.TestWETH9Transactor.Approve(&asserter, addresses.Rollup, value)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)
	tx, err = tokenBindings.TestWETH9Transactor.Approve(&asserter, chalManagerAddr, value)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)

	tx, err = tokenBindings.TestWETH9Transactor.Transfer(&l1TransactionOpts, evilAsserter.From, seed)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)
	tx, err = tokenBindings.TestWETH9Transactor.Approve(&evilAsserter, addresses.Rollup, value)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)
	tx, err = tokenBindings.TestWETH9Transactor.Approve(&evilAsserter, chalManagerAddr, value)
	Require(t, err)
	EnsureTxSucceeded(ctx, backend, tx)

	return &chaininfo.RollupAddresses{
		Bridge:                 addresses.Bridge,
		Inbox:                  addresses.Inbox,
		SequencerInbox:         addresses.SequencerInbox,
		Rollup:                 addresses.Rollup,
		ValidatorUtils:         addresses.ValidatorUtils,
		ValidatorWalletCreator: addresses.ValidatorWalletCreator,
		DeployedAt:             addresses.DeployedAt,
	}, chain
}

func create2ndNodeWithConfigForBoldProtocol(
	t *testing.T,
	ctx context.Context,
	first *arbnode.Node,
	l1stack *node.Node,
	l1info *BlockchainTestInfo,
	l2InitData *statetransfer.ArbosInitializationInfo,
	nodeConfig *arbnode.Config,
	stackConfig *node.Config,
	stakeTokenAddr common.Address,
) (*ethclient.Client, *arbnode.Node, *solimpl.AssertionChain) {
	fatalErrChan := make(chan error, 10)
	l1rpcClient, err := l1stack.Attach()
	if err != nil {
		Fatal(t, err)
	}
	l1client := ethclient.NewClient(l1rpcClient)
	chainConfig := first.Execution.ArbInterface.BlockChain().Config()
	addresses, assertionChain := deployContractsOnly(t, ctx, l1info, l1client, chainConfig.ChainID, stakeTokenAddr)

	l1info.SetContract("EvilBridge", addresses.Bridge)
	l1info.SetContract("EvilSequencerInbox", addresses.SequencerInbox)
	l1info.SetContract("EvilInbox", addresses.Inbox)

	if nodeConfig == nil {
		nodeConfig = arbnode.ConfigDefaultL1NonSequencerTest()
		nodeConfig.ParentChainReader.OldHeaderTimeout = 10 * time.Minute
	}
	nodeConfig.BatchPoster.DataPoster.MaxMempoolTransactions = 0
	if stackConfig == nil {
		stackConfig = stackConfigForTest(t)
	}
	l2stack, err := node.New(stackConfig)
	Require(t, err)

	l2chainDb, err := l2stack.OpenDatabase("chaindb", 0, 0, "", false)
	Require(t, err)
	l2arbDb, err := l2stack.OpenDatabase("arbdb", 0, 0, "", false)
	Require(t, err)

	AddDefaultValNode(t, ctx, nodeConfig, true)

	dataSigner := signature.DataSignerFromPrivateKey(l1info.GetInfoWithPrivKey("Sequencer").PrivateKey)
	txOpts := l1info.GetDefaultTransactOpts("Sequencer", ctx)

	initReader := statetransfer.NewMemoryInitDataReader(l2InitData)
	initMessage := getInitMessage(ctx, t, l1client, first.DeployInfo)

	l2blockchain, err := execution.WriteOrTestBlockChain(l2chainDb, nil, initReader, chainConfig, initMessage, arbnode.ConfigDefaultL2Test().TxLookupLimit, 0)
	Require(t, err)

	l2node, err := arbnode.CreateNode(ctx, l2stack, l2chainDb, l2arbDb, NewFetcherFromConfig(nodeConfig), l2blockchain, l1client, addresses, &txOpts, &txOpts, dataSigner, fatalErrChan)
	Require(t, err)

	Require(t, l2node.Start(ctx))

	l2client := ClientForStack(t, l2stack)

	StartWatchChanErr(t, ctx, fatalErrChan, l2node)

	return l2client, l2node, assertionChain
}

func makeBoldBatch(
	t *testing.T,
	l2Node *arbnode.Node,
	l2Info *BlockchainTestInfo,
	backend *ethclient.Client,
	sequencer *bind.TransactOpts,
	seqInbox *bridgegen.SequencerInbox,
	seqInboxAddr common.Address,
	messagesPerBatch,
	divergeAtIndex int64,
) {
	ctx := context.Background()

	batchBuffer := bytes.NewBuffer([]byte{})
	for i := int64(0); i < messagesPerBatch; i++ {
		value := i
		if i == divergeAtIndex {
			value++
		}
		err := writeTxToBatch(batchBuffer, l2Info.PrepareTx("Owner", "Destination", 1000000, big.NewInt(value), []byte{}))
		Require(t, err)
	}
	compressed, err := arbcompress.CompressWell(batchBuffer.Bytes())
	Require(t, err)
	message := append([]byte{0}, compressed...)

	seqNum := new(big.Int).Lsh(common.Big1, 256)
	seqNum.Sub(seqNum, common.Big1)
	tx, err := seqInbox.AddSequencerL2BatchFromOrigin0(sequencer, seqNum, message, big.NewInt(1), common.Address{}, big.NewInt(0), big.NewInt(0))
	Require(t, err)
	receipt, err := EnsureTxSucceeded(ctx, backend, tx)
	Require(t, err)

	nodeSeqInbox, err := arbnode.NewSequencerInbox(backend, seqInboxAddr, 0)
	Require(t, err)
	batches, err := nodeSeqInbox.LookupBatchesInRange(ctx, receipt.BlockNumber, receipt.BlockNumber)
	Require(t, err)
	if len(batches) == 0 {
		Fatal(t, "batch not found after AddSequencerL2BatchFromOrigin")
	}
	err = l2Node.InboxTracker.AddSequencerBatches(ctx, backend, batches)
	Require(t, err)
	_, err = l2Node.InboxTracker.GetBatchMetadata(0)
	Require(t, err, "failed to get batch metadata after adding batch:")
}
