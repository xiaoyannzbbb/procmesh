package localhttp

// DTOs for HTTP JSON (snake_case)

type CreateProcessRequest struct {
	OperationID      string      `json:"operation_id"`
	Operator         string      `json:"operator"`
	ExpectedRevision int64       `json:"expected_revision"`
	Spec             ProcessSpec `json:"spec"`
}

type ProcessSpec struct {
	ProcessID   string `json:"process_id"`
	Name        string `json:"name"`
	Command     string `json:"command"`
	Args        []string `json:"args"`
	Instances   int    `json:"instances"`
}

type StartProcessRequest struct {
	OperationID string `json:"operation_id"`
	Operator    string `json:"operator"`
}

type AdoptInstanceRequest struct {
	OperationID string `json:"operation_id"`
	Operator    string `json:"operator"`
	PID         int    `json:"pid"`
}

type ListProcessesResponse struct {
	Processes []ProcessResponse `json:"processes"`
}

type ProcessResponse struct {
	ProcessID string `json:"process_id"`
	Spec      ProcessSpec `json:"spec"`
	Instances []Instance `json:"instances"`
}

type Instance struct {
	InstanceID string `json:"instance_id"`
	Ordinal    int    `json:"ordinal"`
	Desired    string `json:"desired"`
	Observed   string `json:"observed"`
	PID        int    `json:"pid"`
}

type TailLogsResponse struct {
	Lines []string `json:"lines"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}