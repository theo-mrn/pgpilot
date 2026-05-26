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
	Name         string              `yaml:"name"`
	Driver       string              `yaml:"driver"`
	Tool         string              `yaml:"tool"`
	DBVersion    string              `yaml:"db_version,omitempty"`
	Environment  EnvironmentConfig   `yaml:"environment"`
	Schedule     string              `yaml:"schedule"`
	Retention    string              `yaml:"retention"`
	Encrypt      bool                `yaml:"encrypt"`
	Destinations []DestinationConfig `yaml:"destinations"`
	Credentials  CredentialsConfig   `yaml:"credentials"`
}

type EnvironmentConfig struct {
	Type      string `yaml:"type"`
	Namespace string `yaml:"namespace,omitempty"`
}

type DestinationConfig struct {
	Name     string `yaml:"name,omitempty"`     // e.g. "primary", "replica"
	Type     string `yaml:"type"`
	Bucket   string `yaml:"bucket"`
	Region   string `yaml:"region,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Prefix   string `yaml:"prefix,omitempty"`
	// S3 credentials — if empty, falls back to job-level credentials
	S3AccessKey SecretRef `yaml:"s3_access_key,omitempty"`
	S3SecretKey SecretRef `yaml:"s3_secret_key,omitempty"`
}

type CredentialsConfig struct {
	DBPassword   SecretRef `yaml:"db_password"`
	DBUser       SecretRef `yaml:"db_user,omitempty"`
	DBName       SecretRef `yaml:"db_name,omitempty"`
	DBHost       string    `yaml:"db_host,omitempty"`
	DBPort       int       `yaml:"db_port,omitempty"`
	AgePublicKey string    `yaml:"age_public_key,omitempty"`
}

type SecretRef struct {
	From string `yaml:"from"`
}
