package wire

// QualityReport is a monitor's signed quality measurement described in docs/09-quality.md.
type QualityReport struct {
	ReportID               string   `json:"report_id"`
	MonitorID              string   `json:"monitor_id"`
	ProviderID             string   `json:"provider_id"`
	PeriodStart            string   `json:"period_start"`
	PeriodEnd              string   `json:"period_end"`
	CreatedAt              string   `json:"created_at"`
	MethodologyDigest      string   `json:"methodology_digest"`
	ObservationsDigest     string   `json:"observations_digest"`
	SampleSeed             string   `json:"sample_seed"`
	SampleSeedCommitment   string   `json:"sample_seed_commitment"`
	SampleCoverage         string   `json:"sample_coverage"`
	AvailabilityAttempts   int64    `json:"availability_attempts"`
	AvailabilitySuccesses  int64    `json:"availability_successes"`
	RetrievalAttempts      int64    `json:"retrieval_attempts"`
	RetrievalPasses        int64    `json:"retrieval_passes"`
	RestoreAttempts        int64    `json:"restore_attempts"`
	RestorePasses          int64    `json:"restore_passes"`
	LatencyP50Ms           *int64   `json:"latency_p50_ms"`
	LatencyP95Ms           *int64   `json:"latency_p95_ms"`
	LatencyP99Ms           *int64   `json:"latency_p99_ms"`
	ReplicaOperators       *int64   `json:"replica_operators"`
	IndependentControllers *int64   `json:"independent_controllers"`
	Sponsors               []string `json:"sponsors"`
	Conflicts              string   `json:"conflicts"`
	MonitorSignature       string   `json:"monitor_signature"`
	SampleFrameDigest      string   `json:"sample_frame_digest"`
}

// QualityObservationSet is a monitor's observation record described in docs/09-quality.md.
type QualityObservationSet struct {
	ProviderID   string               `json:"provider_id"`
	MonitorID    string               `json:"monitor_id"`
	PeriodStart  string               `json:"period_start"`
	PeriodEnd    string               `json:"period_end"`
	Observations []QualityObservation `json:"observations"`
}

// QualityObservation is an observations item in QualityObservationSet, described in docs/09-quality.md.
type QualityObservation struct {
	Kind         string  `json:"kind"`
	MessageID    *string `json:"message_id"`
	StartedAt    string  `json:"started_at"`
	DurationMs   int64   `json:"duration_ms"`
	Outcome      string  `json:"outcome"`
	HTTPStatus   *int64  `json:"http_status"`
	WithinLimits bool    `json:"within_limits"`
}

// QualityReportsPage is the report response described in docs/09-quality.md.
type QualityReportsPage struct {
	Reports []QualityReport `json:"reports"`
}
