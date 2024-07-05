// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package daprovider

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/das/zerogravity"
	"github.com/offchainlabs/nitro/util/blobs"
)

type Reader interface {
	// IsValidHeaderByte returns true if the given headerByte has bits corresponding to the DA provider
	IsValidHeaderByte(headerByte byte) bool

	// RecoverPayloadFromBatch fetches the underlying payload from the DA provider given the batch header information
	RecoverPayloadFromBatch(
		ctx context.Context,
		batchNum uint64,
		batchBlockHash common.Hash,
		sequencerMsg []byte,
		preimageRecorder PreimageRecorder,
		validateSeqMsg bool,
	) ([]byte, error)
}

// NewReaderForDAS is generally meant to be only used by nitro.
// DA Providers should implement methods in the Reader interface independently
func NewReaderForDAS(dasReader DASReader, keysetFetcher DASKeysetFetcher) *readerForDAS {
	return &readerForDAS{
		dasReader:     dasReader,
		keysetFetcher: keysetFetcher,
	}
}

type readerForDAS struct {
	dasReader     DASReader
	keysetFetcher DASKeysetFetcher
}

func (d *readerForDAS) IsValidHeaderByte(headerByte byte) bool {
	return IsDASMessageHeaderByte(headerByte)
}

func (d *readerForDAS) RecoverPayloadFromBatch(
	ctx context.Context,
	batchNum uint64,
	batchBlockHash common.Hash,
	sequencerMsg []byte,
	preimageRecorder PreimageRecorder,
	validateSeqMsg bool,
) ([]byte, error) {
	return RecoverPayloadFromDasBatch(ctx, batchNum, sequencerMsg, d.dasReader, d.keysetFetcher, preimageRecorder, validateSeqMsg)
}

// NewReaderForBlobReader is generally meant to be only used by nitro.
// DA Providers should implement methods in the Reader interface independently
func NewReaderForBlobReader(blobReader BlobReader) *readerForBlobReader {
	return &readerForBlobReader{blobReader: blobReader}
}

type readerForBlobReader struct {
	blobReader BlobReader
}

func (b *readerForBlobReader) IsValidHeaderByte(headerByte byte) bool {
	return IsBlobHashesHeaderByte(headerByte)
}

func (b *readerForBlobReader) RecoverPayloadFromBatch(
	ctx context.Context,
	batchNum uint64,
	batchBlockHash common.Hash,
	sequencerMsg []byte,
	preimageRecorder PreimageRecorder,
	validateSeqMsg bool,
) ([]byte, error) {
	blobHashes := sequencerMsg[41:]
	if len(blobHashes)%len(common.Hash{}) != 0 {
		return nil, ErrInvalidBlobDataFormat
	}
	versionedHashes := make([]common.Hash, len(blobHashes)/len(common.Hash{}))
	for i := 0; i*32 < len(blobHashes); i += 1 {
		copy(versionedHashes[i][:], blobHashes[i*32:(i+1)*32])
	}
	kzgBlobs, err := b.blobReader.GetBlobs(ctx, batchBlockHash, versionedHashes)
	if err != nil {
		return nil, fmt.Errorf("failed to get blobs: %w", err)
	}
	if preimageRecorder != nil {
		for i, blob := range kzgBlobs {
			// Prevent aliasing `blob` when slicing it, as for range loops overwrite the same variable
			// Won't be necessary after Go 1.22 with https://go.dev/blog/loopvar-preview
			b := blob
			preimageRecorder(versionedHashes[i], b[:], arbutil.EthVersionedHashPreimageType)
		}
	}
	payload, err := blobs.DecodeBlobs(kzgBlobs)
	if err != nil {
		log.Warn("Failed to decode blobs", "batchBlockHash", batchBlockHash, "versionedHashes", versionedHashes, "err", err)
		return nil, nil
	}
	return payload, nil
}

// NewDAProviderBlobReader is generally meant to be only used by nitro.
// DA Providers should implement methods in the DataAvailabilityProvider interface independently
func NewDAProviderZg(zgReader ZgDataAvailabilityReader) *dAProviderForZg {
	return &dAProviderForZg{
		zgReader: zgReader,
	}
}

type dAProviderForZg struct {
	zgReader ZgDataAvailabilityReader
}

func (b *dAProviderForZg) IsValidHeaderByte(headerByte byte) bool {
	return IsZgMessageHeaderByte(headerByte)
}

func (b *dAProviderForZg) RecoverPayloadFromBatch(
	ctx context.Context,
	batchNum uint64,
	batchBlockHash common.Hash,
	sequencerMsg []byte,
	preimageRecorder PreimageRecorder,
	validateSeqMsg bool,
) ([]byte, error) {
	log.Info("start recovering payload from zgda")

	// var shaPreimages map[common.Hash][]byte
	// if preimages != nil {
	// 	if preimages[arbutil.Sha2_256PreimageType] == nil {
	// 		preimages[arbutil.Sha2_256PreimageType] = make(map[common.Hash][]byte)
	// 	}
	// 	shaPreimages = preimages[arbutil.Sha2_256PreimageType]
	// }

	blobBytes := sequencerMsg[41:]
	var blobRequestParams []zerogravity.BlobRequestParams
	err := rlp.DecodeBytes(blobBytes, &blobRequestParams)
	if err != nil {
		return nil, err
	}

	blobs, err := b.zgReader.Read(ctx, blobRequestParams)
	if err != nil {
		return nil, fmt.Errorf("failed to get blobs: %w", err)
	}

	// record preimage data
	log.Info("Recording preimage data for zgda")
	shaDataHash := sha256.New()
	shaDataHash.Write(blobBytes)
	// dataHash := shaDataHash.Sum([]byte{})
	// if shaPreimages != nil {
	// 	shaPreimages[common.BytesToHash(dataHash)] = blobs
	// }

	return blobs, nil
}
