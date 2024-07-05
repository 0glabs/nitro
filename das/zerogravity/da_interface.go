package zerogravity

import (
	"context"
)

type DataAvailabilityWriter interface {
	Store(context.Context, []byte) ([]byte, error)
}

type DataAvailabilityReader interface {
	Read(context.Context, []BlobRequestParams) ([]byte, error)
}
