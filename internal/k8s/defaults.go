package k8s

import "fmt"

const (
	DefaultRegion  = "us-east-1"
	DefaultPort    = "5432"
	DefaultPGMajor = "16"

	BackupImagePrefix  = "maxwellfaraday/dbpilot-backup"
	WALImage           = "maxwellfaraday/dbpilot-wal:latest"
	WALDeploymentName  = "dbpilot-wal-agent"
	ReplicationSecret  = "dbpilot-replication-credentials"
	ManagedByLabel     = "app.kubernetes.io/managed-by"
	ManagedByValue     = "dbpilot"
)

// jobImage returns the backup image for the given Postgres major version.
func jobImage(pgVersion string) string {
	if pgVersion == "" {
		pgVersion = DefaultPGMajor
	}
	return BackupImagePrefix + ":pg" + pgVersion
}

// defaultRegion returns dest.Region or DefaultRegion if empty.
func defaultRegion(region string) string {
	if region == "" {
		return DefaultRegion
	}
	return region
}

// defaultPort returns the port as string, falling back to DefaultPort.
func defaultPort(port int) string {
	if port == 0 {
		return DefaultPort
	}
	return fmt.Sprintf("%d", port)
}
