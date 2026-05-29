package detect

// EnvironmentType represents the runtime environment where a database is running.
type EnvironmentType string

const (
	EnvKubernetes EnvironmentType = "kubernetes"
	EnvDocker     EnvironmentType = "docker"
	EnvSystemd    EnvironmentType = "systemd"
	EnvUnknown    EnvironmentType = "unknown"
)

// DatabaseEngine identifies the database software.
type DatabaseEngine string

const (
	EnginePostgres DatabaseEngine = "postgres"
	EngineMySQL    DatabaseEngine = "mysql"
	EngineMongoDB  DatabaseEngine = "mongodb"
	EngineRedis    DatabaseEngine = "redis"
	EngineUnknown  DatabaseEngine = "unknown"
)

// ConfidenceLevel represents how certain dbpilot is that an instance is a real DB server.
type ConfidenceLevel string

const (
	ConfidenceHigh   ConfidenceLevel = "high"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceLow    ConfidenceLevel = "low"
	ConfidenceNone   ConfidenceLevel = "none"
)

// DetectedInstance represents a database instance found during environment scan.
type DetectedInstance struct {
	Environment  EnvironmentType
	Engine       DatabaseEngine
	EngineVersion string // e.g. "16", "15"
	// Kubernetes-specific
	Namespace   string
	PodName     string
	Container   string
	ServiceName string // K8s service to connect to (e.g. times-postgres-rw for CNPG)
	IsCNPG      bool   // true if managed by CloudNativePG
	// Human-readable identifier
	DisplayName string
	// Signals used to identify the engine (e.g. image name, env vars)
	DetectionSignals []string
	Score            int
	Confidence       ConfidenceLevel
}

func confidenceFromScore(score int) ConfidenceLevel {
	switch {
	case score >= 6:
		return ConfidenceHigh
	case score >= 3:
		return ConfidenceMedium
	case score >= 1:
		return ConfidenceLow
	default:
		return ConfidenceNone
	}
}
