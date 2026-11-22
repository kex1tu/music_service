package audit

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/censorcheck"
)

var ErrAuditState = errors.New("invalid censorship audit state")

type CensorshipOptions struct {
	Path       string            `json:"path"`
	Paths      []string          `json:"paths"`
	References map[string]string `json:"references,omitempty"`
	Provider   string            `json:"provider"`
}

type CensorshipEvidence struct {
	Start      float64                 `json:"start"`
	End        float64                 `json:"end"`
	Kind       string                  `json:"kind"`
	Expected   string                  `json:"expected,omitempty"`
	Heard      string                  `json:"heard,omitempty"`
	Origin     string                  `json:"origin"`
	Reference  string                  `json:"reference,omitempty"`
	Transcript *censorcheck.Transcript `json:"transcript,omitempty"`
}

type CensorshipResult struct {
	AudioHash          string               `json:"audio_hash,omitempty"`
	ReferenceAudioHash string               `json:"reference_audio_hash,omitempty"`
	Verdict            string               `json:"verdict"`
	Algorithm          string               `json:"algorithm"`
	Coverage           string               `json:"coverage"`
	Limitations        []string             `json:"limitations"`
	Evidence           []CensorshipEvidence `json:"evidence"`
	CheckedWindows     int                  `json:"checked_windows"`
	TotalWindows       int                  `json:"total_windows"`
}

type CensorshipDecision struct {
	RunID     string `json:"run_id"`
	Decision  string `json:"decision"`
	DecidedAt string `json:"decided_at"`
}

type CensorshipItem struct {
	ID               int64               `json:"id"`
	Path             string              `json:"path"`
	Reference        string              `json:"reference"`
	CurrentPath      string              `json:"current_path"`
	CurrentReference string              `json:"current_reference"`
	State            string              `json:"state"`
	Verdict          string              `json:"verdict"`
	Result           CensorshipResult    `json:"result"`
	Error            string              `json:"error"`
	Decision         string              `json:"manual_decision"`
	Note             string              `json:"manual_note"`
	DecidedAt        string              `json:"decided_at,omitempty"`
	DecisionVerified bool                `json:"decision_verified"`
	PreviousDecision *CensorshipDecision `json:"previous_decision,omitempty"`
}

type CensorshipRun struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Provider string `json:"provider"`
	Created  string `json:"created_at"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Errors   int    `json:"errors"`
}

type CensorshipAudit struct {
	db           *sql.DB
	sb           *sandbox.PathSandbox
	clouds       map[string]censorcheck.Transcriber
	dailySeconds int
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	wake         chan struct{}
	mu           sync.Mutex
	activeRun    string
	activeCancel context.CancelFunc
}
