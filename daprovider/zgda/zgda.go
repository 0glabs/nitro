package zgda

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	pb "github.com/0glabs/0g-da-client/api/grpc/disperser"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/offchainlabs/nitro/daprovider"
)

const (
	RequestTimeout = 180 * time.Second
	SleepDuration  = 3 * time.Second
	MaxRetries     = 90
)

var blobStoreFailure = errors.New("store blob failed")

type BlobRequestParams struct {
	DataRoot []byte
	Epoch    uint64
	QuorumId uint64
}

type ZgConfig struct {
	Enable      bool   `koanf:"enable"`
	Address     string `koanf:"address"`
	MaxBlobSize int    `koanf:"max-blob-size"`
}

type ZgDA struct {
	Client pb.DisperserClient
	Cfg    ZgConfig
	Conn   *grpc.ClientConn
}

func NewZgDA(cfg ZgConfig) (*ZgDA, error) {
	if !cfg.Enable {
		return nil, errors.New("zgDA not enabled")
	}
	if cfg.Address == "" {
		return nil, errors.New("gRPC address is empty")
	}

	if cfg.MaxBlobSize <= 0 {
		return nil, fmt.Errorf("invalid MaxBlobSize: %d", cfg.MaxBlobSize)
	}

	opts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024 * 1024 * 1024)), // 1 GiB
	}
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	conn, err := grpc.NewClient(cfg.Address, opts...)
	if err != nil {
		log.Error("Unable to connect zg server:", "error", err)
		return nil, err
	}

	return &ZgDA{
		Client: pb.NewDisperserClient(conn),
		Cfg:    cfg,
		Conn:   conn,
	}, nil
}

func (s *ZgDA) Close() error {
	if s.Conn != nil {
		return s.Conn.Close()
	}
	return nil
}

func (s *ZgDA) Store(ctx context.Context, seq []byte) ([]byte, error) {
	totalBlobSize := len(seq)
	requestParams := make([]BlobRequestParams, 0)

	if totalBlobSize > 0 {
		preview := seq
		if len(seq) > 32 {
			preview = seq[:32]
		}
		log.Info("Store BatchL2Data", "data preview", hex.EncodeToString(preview), "len", totalBlobSize)

		for idx := 0; idx < totalBlobSize; idx += s.Cfg.MaxBlobSize {
			var endIdx int
			if totalBlobSize <= idx+s.Cfg.MaxBlobSize {
				endIdx = totalBlobSize
			} else {
				endIdx = idx + s.Cfg.MaxBlobSize
			}

			blob := pb.DisperseBlobRequest{
				Data: seq[idx:endIdx],
			}

			log.Info("Disperse blob range", "from", idx, "to", endIdx)
			requestId, err := s.storeBlob(ctx, &blob)
			if err != nil {
				return nil, err
			}

			log.Info("Disperse request id", "id", hex.EncodeToString(requestId))
			requestParam, err := s.waitBlobConfirmed(ctx, requestId)
			if err != nil {
				return nil, fmt.Errorf("store blob failed: %w", err)
			}

			requestParams = append(requestParams, *requestParam)
		}
	}

	rlpEncode, err := rlp.EncodeToBytes(requestParams)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 0)
	buf = append(buf, daprovider.ZgDAMessageHeaderFlag)
	buf = append(buf, rlpEncode...)
	return buf, nil
}

func (s *ZgDA) storeBlob(ctx context.Context, blob *pb.DisperseBlobRequest) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	blobReply, err := s.Client.DisperseBlob(ctx, blob)
	if err != nil {
		log.Warn("Disperse blob error", "err", err)
		return nil, err
	}

	requestId := blobReply.GetRequestId()
	return requestId, nil
}

func (s *ZgDA) Read(ctx context.Context, requestParams []BlobRequestParams) ([]byte, error) {
	var buf bytes.Buffer

	for _, requestParam := range requestParams {
		log.Info("Requesting data from zgDA", "param", requestParam)

		data, err := s.retrieveBlob(ctx, requestParam)
		if err != nil {
			log.Error("Failed to retrieve blob", "error", err)
			return nil, err
		}

		buf.Write(data)
	}

	return buf.Bytes(), nil
}

func (s *ZgDA) retrieveBlob(ctx context.Context, requestParam BlobRequestParams) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	retrieveBlobReply, err := s.Client.RetrieveBlob(ctx, &pb.RetrieveBlobRequest{
		StorageRoot: requestParam.DataRoot,
		Epoch:       requestParam.Epoch,
		QuorumId:    requestParam.QuorumId,
	})

	if err != nil {
		return nil, err
	}

	return retrieveBlobReply.GetData(), nil
}

func (s *ZgDA) waitBlobConfirmed(ctx context.Context, requestId []byte) (*BlobRequestParams, error) {
	var lastErr error

	for retryCount := 0; retryCount < MaxRetries; retryCount++ {
		blobRequest, err := s.waitBlob(ctx, requestId)
		if err == nil {
			return blobRequest, nil
		}

		log.Error("failed to get blob status", "err", err)

		if errors.Is(err, blobStoreFailure) {
			return nil, fmt.Errorf("failed to get blob status: %w", err)
		}

		lastErr = err
		sleepTime := time.Duration(math.Pow(1.5, float64(retryCount)))*time.Second + SleepDuration
		if sleepTime > 60*time.Second {
			sleepTime = 60 * time.Second
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleepTime):
			continue
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unexpected exit from retry loop for request %x", requestId)
	}
	return nil, lastErr
}

func (s *ZgDA) waitBlob(ctx context.Context, requestId []byte) (*BlobRequestParams, error) {
	ctxWithTimeout, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	statusReply, err := s.Client.GetBlobStatus(ctxWithTimeout, &pb.BlobStatusRequest{
		RequestId: requestId,
	})
	if err != nil {
		return nil, err
	}

	log.Info("Blob status reply", "status", statusReply.GetStatus())
	if statusReply.GetStatus() == pb.BlobStatus_CONFIRMED || statusReply.GetStatus() == pb.BlobStatus_FINALIZED {
		blobInfo := statusReply.GetInfo()
		dataRoot := blobInfo.BlobHeader.GetStorageRoot()
		epoch := blobInfo.BlobHeader.GetEpoch()
		quorumId := blobInfo.BlobHeader.GetQuorumId()

		return &BlobRequestParams{
			DataRoot: dataRoot,
			Epoch:    epoch,
			QuorumId: quorumId,
		}, nil
	}

	if statusReply.GetStatus() == pb.BlobStatus_FAILED {
		return nil, blobStoreFailure
	}

	return nil, errors.New("blob status not confirmed")
}
