package telemetry

import (
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
)

// Dimensions identify the execution scope for accounting. Empty dimensions
// are simply not indexed, allowing the same collector in ephemeral runs.
type Dimensions struct {
	RunID      string `json:"run_id,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	TenantID   string `json:"tenant_id,omitempty"`
}

// Measurement is one additive execution observation. Usage and Cost are
// aggregated exactly as reported; callers should record estimated context
// separately through EstimatedContextTokens when appropriate.
type Measurement struct {
	Dimensions             Dimensions    `json:"dimensions"`
	Usage                  ai.Usage      `json:"usage,omitempty"`
	Cost                   float64       `json:"cost,omitempty"`
	Latency                time.Duration `json:"latency,omitempty"`
	TimeToFirstToken       time.Duration `json:"time_to_first_token,omitempty"`
	ToolDuration           time.Duration `json:"tool_duration,omitempty"`
	EstimatedContextTokens int64         `json:"estimated_context_tokens,omitempty"`
	Retries                int64         `json:"retries,omitempty"`
	Compactions            int64         `json:"compactions,omitempty"`
	Aborts                 int64         `json:"aborts,omitempty"`
	ProviderErrors         int64         `json:"provider_errors,omitempty"`
}

// Totals is the cumulative value for one dimension bucket.
type Totals struct {
	Usage                  ai.Usage      `json:"usage"`
	Cost                   float64       `json:"cost"`
	Latency                time.Duration `json:"latency"`
	TimeToFirstToken       time.Duration `json:"time_to_first_token"`
	ToolDuration           time.Duration `json:"tool_duration"`
	EstimatedContextTokens int64         `json:"estimated_context_tokens"`
	Retries                int64         `json:"retries"`
	Compactions            int64         `json:"compactions"`
	Aborts                 int64         `json:"aborts"`
	ProviderErrors         int64         `json:"provider_errors"`
	Count                  int64         `json:"count"`
}

// Accounting aggregates execution data by each P1 dimension without choosing
// a metrics backend. Exporters can snapshot it periodically or mirror Record
// calls directly to their own system.
type Accounting struct {
	mu         sync.RWMutex
	totals     Totals
	byRun      map[string]Totals
	byTurn     map[string]Totals
	bySession  map[string]Totals
	byProvider map[string]Totals
	byModel    map[string]Totals
	byTool     map[string]Totals
	byTenant   map[string]Totals
}

// NewAccounting creates a concurrency-safe collector.
func NewAccounting() *Accounting {
	return &Accounting{byRun: map[string]Totals{}, byTurn: map[string]Totals{}, bySession: map[string]Totals{}, byProvider: map[string]Totals{}, byModel: map[string]Totals{}, byTool: map[string]Totals{}, byTenant: map[string]Totals{}}
}

// Record adds one measurement to every populated dimension bucket.
func (a *Accounting) Record(measurement Measurement) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.totals = addTotals(a.totals, measurement)
	recordDimension(a.byRun, measurement.Dimensions.RunID, measurement)
	recordDimension(a.byTurn, measurement.Dimensions.TurnID, measurement)
	recordDimension(a.bySession, measurement.Dimensions.SessionID, measurement)
	recordDimension(a.byProvider, measurement.Dimensions.ProviderID, measurement)
	recordDimension(a.byModel, measurement.Dimensions.ModelID, measurement)
	recordDimension(a.byTool, measurement.Dimensions.ToolName, measurement)
	recordDimension(a.byTenant, measurement.Dimensions.TenantID, measurement)
}

func recordDimension(index map[string]Totals, key string, measurement Measurement) {
	if key == "" {
		return
	}
	index[key] = addTotals(index[key], measurement)
}

func addTotals(total Totals, m Measurement) Totals {
	total.Usage.InputTokens += m.Usage.InputTokens
	total.Usage.OutputTokens += m.Usage.OutputTokens
	total.Usage.ReasoningTokens += m.Usage.ReasoningTokens
	total.Usage.CacheReadTokens += m.Usage.CacheReadTokens
	total.Usage.CacheWriteTokens += m.Usage.CacheWriteTokens
	total.Usage.TotalTokens += m.Usage.TotalTokens
	total.Usage.Cost.Input += m.Usage.Cost.Input
	total.Usage.Cost.Output += m.Usage.Cost.Output
	total.Usage.Cost.CacheRead += m.Usage.Cost.CacheRead
	total.Usage.Cost.CacheWrite += m.Usage.Cost.CacheWrite
	total.Usage.Cost.Total += m.Usage.Cost.Total
	total.Cost += m.Cost
	total.Latency += m.Latency
	total.TimeToFirstToken += m.TimeToFirstToken
	total.ToolDuration += m.ToolDuration
	total.EstimatedContextTokens += m.EstimatedContextTokens
	total.Retries += m.Retries
	total.Compactions += m.Compactions
	total.Aborts += m.Aborts
	total.ProviderErrors += m.ProviderErrors
	total.Count++
	return total
}

// Snapshot is an immutable copy that callers can export without holding the
// collector lock.
type Snapshot struct {
	Total      Totals            `json:"total"`
	ByRun      map[string]Totals `json:"by_run,omitempty"`
	ByTurn     map[string]Totals `json:"by_turn,omitempty"`
	BySession  map[string]Totals `json:"by_session,omitempty"`
	ByProvider map[string]Totals `json:"by_provider,omitempty"`
	ByModel    map[string]Totals `json:"by_model,omitempty"`
	ByTool     map[string]Totals `json:"by_tool,omitempty"`
	ByTenant   map[string]Totals `json:"by_tenant,omitempty"`
}

// Snapshot returns all aggregation dimensions.
func (a *Accounting) Snapshot() Snapshot {
	if a == nil {
		return Snapshot{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Snapshot{Total: a.totals, ByRun: cloneTotals(a.byRun), ByTurn: cloneTotals(a.byTurn), BySession: cloneTotals(a.bySession), ByProvider: cloneTotals(a.byProvider), ByModel: cloneTotals(a.byModel), ByTool: cloneTotals(a.byTool), ByTenant: cloneTotals(a.byTenant)}
}

func cloneTotals(in map[string]Totals) map[string]Totals {
	out := make(map[string]Totals, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
