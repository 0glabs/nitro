// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package arbtest

import (
	"context"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/arbos/l2pricing"
	"github.com/offchainlabs/nitro/util"
	"math/big"
	"os"
	"testing"
	"time"
)

func TestSync(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	var transferGas = util.NormalizeL2GasForL1GasInitial(800_000, params.GWei) // include room for aggregator L1 costs

	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	builder.L2Info = NewBlockChainTestInfo(
		t,
		types.NewArbitrumSigner(types.NewLondonSigner(builder.chainConfig.ChainID)), big.NewInt(l2pricing.InitialBaseFeeWei*2),
		transferGas,
	)
	cleanup := builder.Build(t)
	defer cleanup()

	builder.BridgeBalance(t, "Faucet", big.NewInt(1).Mul(big.NewInt(params.Ether), big.NewInt(10000)))

	builder.L2Info.GenerateAccount("BackgroundUser")
	for {
		tx := builder.L2Info.PrepareTx("Faucet", "BackgroundUser", builder.L2Info.TransferGas, big.NewInt(1), nil)
		err := builder.L2.Client.SendTransaction(ctx, tx)
		Require(t, err)
		_, err = builder.L2.EnsureTxSucceeded(tx)
		Require(t, err)
		count, err := builder.L2.ConsensusNode.InboxTracker.GetBatchCount()
		Require(t, err)
		if count > 10 {
			break
		}
	}
	<-time.After(time.Second * 5)

	count, err := builder.L2.ConsensusNode.InboxTracker.GetBatchCount()
	Require(t, err)
	nodeConfig := builder.nodeConfig
	nodeConfig.InboxReader.FirstBatchToKeep = count

	err = os.RemoveAll(builder.l2StackConfig.ResolvePath("arbitrumdata"))
	Require(t, err)

	builder.L2.cleanup()

	nodeB, cleanupB := builder.Build2ndNode(t, &SecondNodeParams{stackConfig: builder.l2StackConfig, nodeConfig: nodeConfig})
	defer cleanupB()
	for {
		tx := builder.L2Info.PrepareTx("Faucet", "BackgroundUser", builder.L2Info.TransferGas, big.NewInt(1), nil)
		err := nodeB.Client.SendTransaction(ctx, tx)
		Require(t, err)
		_, err = nodeB.EnsureTxSucceeded(tx)
		Require(t, err)
		count, err := nodeB.ConsensusNode.InboxTracker.GetBatchCount()
		Require(t, err)
		if count > 20 {
			break
		}
	}

}
