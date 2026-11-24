package devicewire

import (
	"context"
	"sync/atomic"
)

const (
	TaskExecutionCapability     = "task_execution"
	LegacyTaskExecutionFallback = "legacy_task_execution"
)

type taskExecutionWaitKey struct{}
type taskExecutionEvidenceKey struct{}
type taskExecutionEvidence struct{ legacy atomic.Bool }

// WithTaskExecutionWait marks a scheduled dispatch whose pre-delivery compatibility refusals may wait for a node update.
func WithTaskExecutionWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskExecutionWaitKey{}, true)
}

func TaskExecutionWaitEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(taskExecutionWaitKey{}).(bool)
	return enabled
}

// WithTaskExecutionEvidence records what the device boundary actually did, not what the task was eligible to do.
func WithTaskExecutionEvidence(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskExecutionEvidenceKey{}, &taskExecutionEvidence{})
}

func MarkLegacyTaskExecution(ctx context.Context) {
	if evidence, ok := ctx.Value(taskExecutionEvidenceKey{}).(*taskExecutionEvidence); ok {
		evidence.legacy.Store(true)
	}
}

func LegacyTaskExecutionUsed(ctx context.Context) bool {
	evidence, ok := ctx.Value(taskExecutionEvidenceKey{}).(*taskExecutionEvidence)
	return ok && evidence.legacy.Load()
}
