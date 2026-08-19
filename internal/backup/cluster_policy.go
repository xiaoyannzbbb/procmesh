package backup

// PolicyRecord mirrors the control-plane JSON shape without importing internal/control.
type PolicyRecord struct {
	PolicyID, Name           string
	Enabled                  bool
	ScheduleCron, Timezone   string
	TargetSelector           string
	TargetIDs                []string
	Sink, DestinationProfile string
	RetentionKeepLast        int
	RetentionKeepDays        int
	RetentionMaxBytes        int64
	TimeoutSeconds           int
	MaxConcurrency           int
	UnavailablePolicy        string
	Revision                 int64
}

type Policy struct {
	PolicyID, Name           string
	Enabled                  bool
	ScheduleCron, Timezone   string
	TargetSelector           string
	TargetIDs                []string
	Sink, DestinationProfile string
	RetentionKeepLast        int
	RetentionKeepDays        int
	RetentionMaxBytes        int64
	TimeoutSeconds           int
	MaxConcurrency           int
	UnavailablePolicy        string
	Revision                 int64
}

func PolicyFromRecord(r PolicyRecord) Policy {
	return Policy{
		PolicyID: r.PolicyID, Name: r.Name, Enabled: r.Enabled,
		ScheduleCron: r.ScheduleCron, Timezone: r.Timezone,
		TargetSelector: r.TargetSelector, TargetIDs: append([]string(nil), r.TargetIDs...),
		Sink: r.Sink, DestinationProfile: r.DestinationProfile,
		RetentionKeepLast: r.RetentionKeepLast, RetentionKeepDays: r.RetentionKeepDays,
		RetentionMaxBytes: r.RetentionMaxBytes, TimeoutSeconds: r.TimeoutSeconds,
		MaxConcurrency: r.MaxConcurrency, UnavailablePolicy: r.UnavailablePolicy,
		Revision: r.Revision,
	}
}
