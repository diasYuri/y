package policy

// This package remains as a private compatibility facade for the binary.
// The canonical policy contracts live in pkg/policy so SDK packages never
// depend on internal implementation details.
import publicpolicy "github.com/diasYuri/y/pkg/policy"

type DecisionKind = publicpolicy.DecisionKind
type ApprovalMode = publicpolicy.ApprovalMode
type ApprovalState = publicpolicy.ApprovalState
type ApprovalResolution = publicpolicy.ApprovalResolution
type ApprovalRequest = publicpolicy.ApprovalRequest
type Decision = publicpolicy.Decision
type Request = publicpolicy.Request
type Config = publicpolicy.Config
type Engine = publicpolicy.Engine
type Func = publicpolicy.Func

const (
	DecisionAllow           = publicpolicy.DecisionAllow
	DecisionDeny            = publicpolicy.DecisionDeny
	DecisionRequireApproval = publicpolicy.DecisionRequireApproval
	ApprovalModeHeadless    = publicpolicy.ApprovalModeHeadless
	ApprovalPending         = publicpolicy.ApprovalPending
	ApprovalApproved        = publicpolicy.ApprovalApproved
	ApprovalDenied          = publicpolicy.ApprovalDenied
)

func DefaultConfig() Config { return publicpolicy.DefaultConfig() }

func NewEngine(cfg Config) Engine { return publicpolicy.NewEngine(cfg) }
