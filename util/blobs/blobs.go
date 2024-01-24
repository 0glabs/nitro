// Copyright 2023-2024, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package blobs

import (
	"bytes"
	"crypto/sha256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// EncodeBlobs takes in raw bytes data to convert into blobs used for KZG commitment EIP-4844
// transactions on Ethereum.
func EncodeBlobs(data []byte) ([]kzg4844.Blob, error) {
	data, err := rlp.EncodeToBytes(data)
	if err != nil {
		return nil, err
	}
	blobs := []kzg4844.Blob{{}}
	blobIndex := 0
	fieldIndex := -1
	for i := 0; i < len(data); i += 31 {
		fieldIndex++
		if fieldIndex == params.BlobTxFieldElementsPerBlob {
			blobs = append(blobs, kzg4844.Blob{})
			blobIndex++
			fieldIndex = 0
		}
		max := i + 31
		if max > len(data) {
			max = len(data)
		}
		copy(blobs[blobIndex][fieldIndex*32+1:], data[i:max])
	}
	return blobs, nil
}

// DecodeBlobs decodes blobs into the batch data encoded in them.
func DecodeBlobs(blobs []kzg4844.Blob) ([]byte, error) {
	var rlpData []byte
	for _, blob := range blobs {
		for fieldIndex := 0; fieldIndex < params.BlobTxFieldElementsPerBlob; fieldIndex++ {
			rlpData = append(rlpData, blob[fieldIndex*32+1:(fieldIndex+1)*32]...)
		}
	}
	var outputData []byte
	err := rlp.Decode(bytes.NewReader(rlpData), &outputData)
	return outputData, err
}

func CommitmentToVersionedHash(commitment kzg4844.Commitment) common.Hash {
	// As per the EIP-4844 spec, the versioned hash is the SHA-256 hash of the commitment with the first byte set to 1.
	hash := sha256.Sum256(commitment[:])
	hash[0] = 1
	return hash
}

// Return KZG commitments, proofs, and versioned hashes that corresponds to these blobs
func ComputeCommitmentsProofsAndHashes(blobs []kzg4844.Blob) ([]kzg4844.Commitment, []kzg4844.Proof, []common.Hash, error) {
	commitments := make([]kzg4844.Commitment, len(blobs))
	proofs := make([]kzg4844.Proof, len(blobs))
	versionedHashes := make([]common.Hash, len(blobs))

	for i := range blobs {
		var err error
		commitments[i], err = kzg4844.BlobToCommitment(blobs[i])
		if err != nil {
			return nil, nil, nil, err
		}
		proofs[i], err = kzg4844.ComputeBlobProof(blobs[i], commitments[i])
		if err != nil {
			return nil, nil, nil, err
		}
		versionedHashes[i] = CommitmentToVersionedHash(commitments[i])
	}

	return commitments, proofs, versionedHashes, nil
}
