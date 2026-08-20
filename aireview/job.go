package aireview

import "time"

// Verdicts produced by the Stage-0 pre-validation gate.
const (
	VerdictNone        = ""
	VerdictPlaceholder = "placeholder"
	VerdictMalformed   = "malformed"
	VerdictRevoked     = "revoked"
	VerdictLikelyReal  = "likely-real"
	VerdictReal        = "real"
	VerdictTestFixture = "test-fixture"
)

// StageCost records the estimated token/cost usage of one workflow stage.
type StageCost struct {
	Name       string  `json:"name"`
	TokensEst  int     `json:"tokens_est"`
	CostEstUSD float64 `json:"cost_est_usd"`
}

// CostInfo is the per-job cost ledger (estimates written by the DSH workflow).
type CostInfo struct {
	TotalEstUSD float64     `json:"total_est_usd"`
	Stages      []StageCost `json:"stages"`
}

// Job is the persisted state of one AI review job. Field names mirror the
// job.json schema in the spec.
type Job struct {
	ID                string    `json:"id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	RepoURL           string    `json:"repo_url"`
	File              string    `json:"file"`
	Signature         string    `json:"signature"`
	Secret            string    `json:"secret"`
	SecretFingerprint string    `json:"secret_fingerprint"`
	MatchContext      string    `json:"match_context"`
	Stars             int       `json:"stars"`
	Authorized        bool      `json:"authorized"`
	State             State     `json:"state"`
	Stage             int       `json:"stage"`
	StageName         string    `json:"stage_name"`
	Verdict           string    `json:"verdict"`
	ReviewNeeded      bool      `json:"review_needed"`
	CaseID            string    `json:"case_id"`
	ClonePath         string    `json:"clone_path"`
	Cost              CostInfo  `json:"cost"`
	Error             string    `json:"error"`
}
