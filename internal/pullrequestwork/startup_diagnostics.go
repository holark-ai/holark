package pullrequestwork

import (
	"context"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func workStartupContext(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		requestID = pullrequestlifecycle.RequestID(ctx)
	}
	return pullrequestlifecycle.WithRequestID(ctx, requestID)
}
