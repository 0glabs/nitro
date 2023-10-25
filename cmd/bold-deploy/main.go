// Copyright 2023, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	protocol "github.com/OffchainLabs/bold/chain-abstraction"
	solimpl "github.com/OffchainLabs/bold/chain-abstraction/sol-implementation"
	retry "github.com/OffchainLabs/bold/runtime"
	"github.com/OffchainLabs/bold/solgen/go/mocksgen"
	rollupgen "github.com/OffchainLabs/bold/solgen/go/rollupgen"
	challenge_testing "github.com/OffchainLabs/bold/testing"
	"github.com/OffchainLabs/bold/testing/setup"

	"github.com/offchainlabs/nitro/cmd/chaininfo"
	"github.com/offchainlabs/nitro/cmd/genericconf"
	"github.com/offchainlabs/nitro/solgen/go/precompilesgen"
	"github.com/offchainlabs/nitro/util/headerreader"
	"github.com/offchainlabs/nitro/validator/server_common"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/cmd/util"
)

func main() {
	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	glogger.Verbosity(log.LvlDebug)
	log.Root().SetHandler(glogger)
	log.Info("deploying rollup")

	ctx := context.Background()

	l1conn := flag.String("l1conn", "", "l1 connection")
	l1keystore := flag.String("l1keystore", "", "l1 private key store")
	l1privatekey := flag.String("l1privatekey", "", "l1 private key")
	deployAccount := flag.String("l1DeployAccount", "", "l1 seq account to use (default is first account in keystore)")
	ownerAddressString := flag.String("ownerAddress", "", "the rollup owner's address")
	sequencerAddressString := flag.String("sequencerAddress", "", "the sequencer's address")
	loserEscrowAddressString := flag.String("loserEscrowAddress", "", "the address which half of challenge loser's funds accumulate at")
	wasmmoduleroot := flag.String("wasmmoduleroot", "", "WASM module root hash")
	wasmrootpath := flag.String("wasmrootpath", "", "path to machine folders")
	l1passphrase := flag.String("l1passphrase", "passphrase", "l1 private key file passphrase")
	outfile := flag.String("l1deployment", "deploy.json", "deployment output json file")
	l1ChainIdUint := flag.Uint64("l1chainid", 1337, "L1 chain ID")
	l2ChainConfig := flag.String("l2chainconfig", "l2_chain_config.json", "L2 chain config json file")
	l2ChainName := flag.String("l2chainname", "", "L2 chain name (will be included in chain info output json file)")
	l2ChainInfo := flag.String("l2chaininfo", "l2_chain_info.json", "L2 chain info output json file")
	txTimeout := flag.Duration("txtimeout", 10*time.Minute, "Timeout when waiting for a transaction to be included in a block")
	prod := flag.Bool("prod", false, "Whether to configure the rollup for production or testing")
	flag.Parse()
	l1ChainId := new(big.Int).SetUint64(*l1ChainIdUint)

	if *prod {
		if *wasmmoduleroot == "" {
			panic("must specify wasm module root when launching prod chain")
		}
	}
	if *l2ChainName == "" {
		panic("must specify l2 chain name")
	}

	var l1TransactionOpts *bind.TransactOpts
	var err error
	if *l1privatekey != "" {
		privKey, err := crypto.HexToECDSA(*l1privatekey)
		if err != nil {
			flag.Usage()
			log.Error("error parsing l1 private key")
			panic(err)
		}
		l1TransactionOpts, err = bind.NewKeyedTransactorWithChainID(privKey, l1ChainId)
		if err != nil {
			flag.Usage()
			log.Error("error creating l1 tx opts")
			panic(err)
		}
	} else {
		wallet := genericconf.WalletConfig{
			Pathname:   *l1keystore,
			Account:    *deployAccount,
			Password:   *l1passphrase,
			PrivateKey: *l1privatekey,
		}
		l1TransactionOpts, _, err = util.OpenWallet("l1", &wallet, l1ChainId)
		if err != nil {
			flag.Usage()
			log.Error("error reading keystore")
			panic(err)
		}
	}

	l1client, err := ethclient.Dial(*l1conn)
	if err != nil {
		flag.Usage()
		log.Error("error creating l1client")
		panic(err)
	}

	if !common.IsHexAddress(*sequencerAddressString) && len(*sequencerAddressString) > 0 {
		panic("specified sequencer address is invalid")
	}
	if !common.IsHexAddress(*ownerAddressString) {
		panic("please specify a valid rollup owner address")
	}
	if *prod && !common.IsHexAddress(*loserEscrowAddressString) {
		panic("please specify a valid loser escrow address")
	}

	sequencerAddress := common.HexToAddress(*sequencerAddressString)
	ownerAddress := common.HexToAddress(*ownerAddressString)
	loserEscrowAddress := common.HexToAddress(*loserEscrowAddressString)
	if sequencerAddress != (common.Address{}) && ownerAddress != l1TransactionOpts.From {
		panic("cannot specify sequencer address if owner is not deployer")
	}

	var moduleRoot common.Hash
	if *wasmmoduleroot == "" {
		locator, err := server_common.NewMachineLocator(*wasmrootpath)
		if err != nil {
			panic(err)
		}
		moduleRoot = locator.LatestWasmModuleRoot()
	} else {
		moduleRoot = common.HexToHash(*wasmmoduleroot)
	}
	if moduleRoot == (common.Hash{}) {
		panic("wasmModuleRoot not found")
	}

	headerReaderConfig := headerreader.DefaultConfig
	headerReaderConfig.TxTimeout = *txTimeout

	chainConfigJson, err := os.ReadFile(*l2ChainConfig)
	if err != nil {
		panic(fmt.Errorf("failed to read l2 chain config file: %w", err))
	}
	var chainConfig params.ChainConfig
	err = json.Unmarshal(chainConfigJson, &chainConfig)
	if err != nil {
		panic(fmt.Errorf("failed to deserialize chain config: %w", err))
	}

	arbSys, _ := precompilesgen.NewArbSys(types.ArbSysAddress, l1client)
	l1Reader, err := headerreader.New(ctx, l1client, func() *headerreader.Config { return &headerReaderConfig }, arbSys)
	if err != nil {
		panic(fmt.Errorf("failed to create header reader: %w", err))
	}
	l1Reader.Start(ctx)
	defer l1Reader.StopAndWait()

	stakeToken, _, tokenBindings, err := mocksgen.DeployTestWETH9(
		l1TransactionOpts,
		l1Reader.Client(),
		"Weth",
		"WETH",
	)
	if err != nil {
		panic(err)
	}
	validatorPriv, err := crypto.HexToECDSA("4186cddd403633d6d845bfbefa87dcffc9152eb8373b97b53e5e8e15b918aba6")
	if err != nil {
		panic(err)
	}
	validatorOpts, err := bind.NewKeyedTransactorWithChainID(validatorPriv, l1ChainId)
	if err != nil {
		panic(err)
	}
	evilValidatorPriv, err := crypto.HexToECDSA("ee3c0bf39d962a78dba87aee083cae443cabc814f93677f302cbabde844237db")
	if err != nil {
		panic(err)
	}
	evilValidatorOpts, err := bind.NewKeyedTransactorWithChainID(evilValidatorPriv, l1ChainId)
	if err != nil {
		panic(err)
	}
	validatorOpts.GasLimit = 1_000_000
	evilValidatorOpts.GasLimit = 1_000_000
	validatorOpts.Value = big.NewInt(100)
	evilValidatorOpts.Value = big.NewInt(100)
	_, err = tokenBindings.Deposit(validatorOpts)
	if err != nil {
		panic(err)
	}
	_, err = tokenBindings.Deposit(evilValidatorOpts)
	if err != nil {
		panic(err)
	}
	validatorOpts.Value = big.NewInt(0)
	evilValidatorOpts.Value = big.NewInt(0)

	miniStake := big.NewInt(1)
	genesisExecutionState := rollupgen.ExecutionState{
		GlobalState:   rollupgen.GlobalState{},
		MachineStatus: 1,
	}
	genesisInboxCount := big.NewInt(0)
	anyTrustFastConfirmer := common.Address{}
	bigStepHeight := uint64(1 << 5)
	smallStepHeight := uint64(1 << 7)
	rollupConfig := challenge_testing.GenerateRollupConfig(
		*prod,
		moduleRoot,
		l1TransactionOpts.From,
		chainConfig.ChainID,
		loserEscrowAddress,
		miniStake,
		stakeToken,
		genesisExecutionState,
		genesisInboxCount,
		anyTrustFastConfirmer,
		challenge_testing.WithLayerZeroHeights(&protocol.LayerZeroHeights{
			BlockChallengeHeight:     32,
			BigStepChallengeHeight:   bigStepHeight,
			SmallStepChallengeHeight: smallStepHeight,
		}),
		challenge_testing.WithNumBigStepLevels(uint8(5)),       // TODO: Hardcoded.
		challenge_testing.WithConfirmPeriodBlocks(uint64(400)), // TODO: Hardcoded to 1000 L1 blocks.
		challenge_testing.WithChainConfig(string(chainConfigJson)),
	)
	deployedAddresses, err := setup.DeployFullRollupStack(
		ctx,
		l1Reader.Client(),
		l1TransactionOpts,
		l1TransactionOpts.From,
		rollupConfig,
		false, // do not use mock bridge.
		false, // do not use a mock one step prover
	)
	if err != nil {
		flag.Usage()
		log.Error("error deploying on l1")
		panic(err)
	}
	assertionChain, err := solimpl.NewAssertionChain(ctx, deployedAddresses.Rollup, l1TransactionOpts, l1Reader.Client())
	if err != nil {
		panic(err)
	}
	chalManager, err := assertionChain.SpecChallengeManager(ctx)
	if err != nil {
		panic(err)
	}
	validatorOpts.Value = big.NewInt(0)
	evilValidatorOpts.Value = big.NewInt(0)
	_, err = tokenBindings.Approve(validatorOpts, deployedAddresses.Rollup, big.NewInt(1000))
	if err != nil {
		panic(err)
	}
	_, err = tokenBindings.Approve(validatorOpts, chalManager.Address(), big.NewInt(1000))
	if err != nil {
		panic(err)
	}
	_, err = tokenBindings.Approve(evilValidatorOpts, deployedAddresses.Rollup, big.NewInt(1000))
	if err != nil {
		panic(err)
	}
	_, err = tokenBindings.Approve(evilValidatorOpts, chalManager.Address(), big.NewInt(1000))
	if err != nil {
		panic(err)
	}

	rollup, err := rollupgen.NewRollupAdminLogicTransactor(deployedAddresses.Rollup, l1Reader.Client())
	if err != nil {
		panic(err)
	}
	_, err = retry.UntilSucceeds[*types.Transaction](ctx, func() (*types.Transaction, error) {
		return rollup.SetMinimumAssertionPeriod(l1TransactionOpts, big.NewInt(1)) // 1 Ethereum block between assertions
	})
	if err != nil {
		panic(err)
	}

	// We then have the validator itself authorize the rollup and challenge manager
	// contracts to spend its stake tokens.
	deployData, err := json.Marshal(deployedAddresses)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*outfile, deployData, 0600); err != nil {
		panic(err)
	}
	parentChainIsArbitrum := l1Reader.IsParentChainArbitrum()
	chainsInfo := []chaininfo.ChainInfo{
		{
			ChainName:             *l2ChainName,
			ParentChainId:         l1ChainId.Uint64(),
			ParentChainIsArbitrum: &parentChainIsArbitrum,
			ChainConfig:           &chainConfig,
			RollupAddresses: &chaininfo.RollupAddresses{
				Bridge:                 deployedAddresses.Bridge,
				Inbox:                  deployedAddresses.Inbox,
				SequencerInbox:         deployedAddresses.SequencerInbox,
				Rollup:                 deployedAddresses.Rollup,
				ValidatorUtils:         deployedAddresses.ValidatorUtils,
				ValidatorWalletCreator: deployedAddresses.ValidatorWalletCreator,
				StakeToken:             stakeToken,
				DeployedAt:             deployedAddresses.DeployedAt,
			},
		},
	}
	chainsInfoJson, err := json.Marshal(chainsInfo)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s\n", chainsInfoJson)
	if err := os.WriteFile(*l2ChainInfo, chainsInfoJson, 0600); err != nil {
		panic(err)
	}
}
