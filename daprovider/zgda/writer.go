package zgda

import (
	"context"
	"errors"
)

func NewWriterForZgda(zgdaWriter ZgDAWriter) *writerForZgDA {
	return &writerForZgDA{zgDAWriter: zgdaWriter}
}

type writerForZgDA struct {
	zgDAWriter ZgDAWriter
}

func (c *writerForZgDA) Store(ctx context.Context, message []byte, timeout uint64, disableFallbackStoreDataOnChain bool) ([]byte, error) {
	msg, err := c.zgDAWriter.Store(ctx, message)
	if err != nil {
		if disableFallbackStoreDataOnChain {
			return nil, errors.New("unable to batch to zgDA and fallback storing data on chain is disabled")
		}
		return nil, err
	}
	message = msg
	return message, nil
}

func (d *writerForZgDA) Type() string {
	return "zgDA"
}
