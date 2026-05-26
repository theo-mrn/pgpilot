package config

// BackupConfig is the top-level structure of backup.yaml.
type BackupConfig struct {
	Global GlobalConfig `yaml:"global"`
	Jobs   []JobConfig  `yaml:"jobs"`
}

type GlobalConfig struct {
	VerifyRestore bool   `yaml:"verify_restore"`
	VerifyTTL     string `yaml:"verify_ttl"`
}

type JobConfig struct {
	Name          string            `yaml:"name"`
	Driver        string            `yaml:"driver"`
	Tool          string            `yaml:"tool"`
	DBVersion     string            `yaml:"db_version,omitempty"` // e.g. "17", "16", "15"
	Environment   EnvironmentConfig `yaml:"environment"`
	Schedule      string            `yaml:"schedule"`
	Retention     string            `yaml:"retention"`
	Encrypt       bool              `yaml:"encrypt"`
	Destination   DestinationConfig `yaml:"destination"`
	Credentials   CredentialsConfig `yaml:"credentials"`
}

type EnvironmentConfig struct {
	Type      string `yaml:"type"`
	Namespace string `yaml:"namespace,omitempty"`
}

type DestinationConfig struct {
	Type     string `yaml:"type"`
	Bucket   string `yaml:"bucket"`
	Region   string `yaml:"region,omitempty"`   // AWS region, empty for MinIO
	Endpoint string `yaml:"endpoint,omitempty"` // empty = AWS S3 native
	Prefix   string `yaml:"prefix,omitempty"`
}

type CredentialsConfig struct {
	DBPassword   SecretRef `yaml:"db_password"`
	DBUser       SecretRef `yaml:"db_user,omitempty"`
	DBName       SecretRef `yaml:"db_name,omitempty"`
	DBHost       string    `yaml:"db_host,omitempty"`
	DBPort       int       `yaml:"db_port,omitempty"` // defaults to 5432
	S3AccessKey  SecretRef `yaml:"s3_access_key"`
	S3SecretKey  SecretRef `yaml:"s3_secret_key"`
	AgePublicKey string    `yaml:"age_public_key,omitempty"`
}

type SecretRef struct {
	From string `yaml:"from"`
}
