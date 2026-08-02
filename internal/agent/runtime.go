// Package agent defines the dependency boundary for Memora-owned Agent
// workflows. Database access is available only through the neutral MSQL port.
package agent

import (
	"context"
	"errors"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

var ErrInvalidDependencies = errors.New("agent dependencies are invalid")

// MSQLExecutor is the only database capability visible to Agent code.
// The interface is owned by the consumer so adapters do not leak their
// concrete runtime or storage types into this package.
type MSQLExecutor interface {
	ExecuteMSQL(context.Context, protocolmsql.Request) (protocolmsql.Envelope, error)
}

// Dependencies contains only dependencies required by the current Runtime.
// Later Agent features add their ports when those behaviors are implemented.
type Dependencies struct {
	MSQL MSQLExecutor
}

// Runtime is the validated Agent-side gateway to injected capabilities.
type Runtime struct {
	msql MSQLExecutor
}

func New(dependencies Dependencies) (*Runtime, error) {
	if dependencies.MSQL == nil {
		return nil, ErrInvalidDependencies
	}
	return &Runtime{msql: dependencies.MSQL}, nil
}

// ExecuteMSQL validates both sides of the neutral protocol boundary. It makes
// exactly one executor call and deliberately owns no retry policy.
func (runtime *Runtime) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	if err := request.Validate(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	envelope, err := runtime.msql.ExecuteMSQL(ctx, request)
	if err != nil {
		return protocolmsql.Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	return envelope, nil
}
