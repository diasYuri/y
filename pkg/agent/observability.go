package agent

import (
	"context"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/telemetry"
)

func (a *Agent) startSpan(ctx context.Context, name string, attributes ...telemetry.Attribute) (context.Context, telemetry.Span) {
	a.mu.Lock()
	tracer := a.tracer
	a.mu.Unlock()
	if tracer == nil {
		return telemetry.NoopTracer{}.Start(ctx, name, attributes...)
	}
	return tracer.Start(ctx, name, attributes...)
}

func (a *Agent) recordAccounting(measurement telemetry.Measurement) {
	a.mu.Lock()
	accounting := a.accounting
	a.mu.Unlock()
	if accounting != nil {
		accounting.Record(measurement)
	}
}

func (a *Agent) accountingDimensions(turn int, model ai.Model, tool string) telemetry.Dimensions {
	a.mu.Lock()
	dimensions := telemetry.Dimensions{RunID: a.runID, SessionID: a.sessionID, ProviderID: string(model.Provider), ModelID: model.ID, ToolName: tool, TenantID: a.policyIdentity.TenantID}
	a.mu.Unlock()
	if turn > 0 && dimensions.RunID != "" {
		dimensions.TurnID = dimensions.RunID + "-turn-" + itoa(turn)
	}
	return dimensions
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
