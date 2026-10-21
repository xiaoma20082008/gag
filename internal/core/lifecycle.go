package core

import (
	"context"
	"io"
)

type Lifecycle interface {
	Name() string
	Startup(ctx context.Context) error
	Shutdown(ctx context.Context) error
	io.Closer
}
