package zgda

import (
	"context"
)

type ZgDAWriter interface {
	Store(context.Context, []byte) ([]byte, error)
}

type ZgDAReader interface {
	Read(context.Context, []BlobRequestParams) ([]byte, error)
}
