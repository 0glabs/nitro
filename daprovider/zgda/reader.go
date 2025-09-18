package zgda

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/daprovider"
)

func NewReaderForZgDA(zgDAReader ZgDAReader) *readerForZgDA {
	return &readerForZgDA{zgDAReader: zgDAReader}
}

type readerForZgDA struct {
	zgDAReader ZgDAReader
}

func (c *readerForZgDA) IsValidHeaderByte(ctx context.Context, headerByte byte) bool {
	return daprovider.IsZgDAMessageHeaderByte(headerByte)
}

func (c *readerForZgDA) RecoverPayloadFromBatch(
	ctx context.Context,
	batchNum uint64,
	batchBlockHash common.Hash,
	sequencerMsg []byte,
	preimages daprovider.PreimagesMap,
	validateSeqMsg bool,
) ([]byte, daprovider.PreimagesMap, error) {
	log.Info("start recovering payload from zgda")

	if preimages == nil {
		preimages = make(daprovider.PreimagesMap)
	}
	preimageRecorder := daprovider.RecordPreimagesTo(preimages)

	blobBytes := sequencerMsg[41:]
	var blobRequestParams []BlobRequestParams
	err := rlp.DecodeBytes(blobBytes, &blobRequestParams)
	if err != nil {
		return nil, nil, err
	}

	blobs, err := c.zgDAReader.Read(ctx, blobRequestParams)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get blobs: %w", err)
	}

	// record preimage data
	log.Info("Recording preimage data for zgda")
	shaDataHash := sha256.New()
	shaDataHash.Write(blobBytes)
	dataHash := shaDataHash.Sum([]byte{})

	if preimageRecorder != nil {
		preimageRecorder(common.BytesToHash(dataHash), blobs, arbutil.Sha2_256PreimageType)
	}

	return blobs, preimages, nil
}
