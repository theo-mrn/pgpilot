package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ValidationError struct {
	Job    string
	Field  string
	Reason string
}

func (e ValidationError) Error() string {
	if e.Job == "" {
		return fmt.Sprintf("global: %s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("job %q: %s: %s", e.Job, e.Field, e.Reason)
}

// Load reads and parses backup.yaml from path.
func Load(path string) (BackupConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BackupConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg BackupConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return BackupConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks that cfg is complete and consistent.
// Returns all errors found, not just the first.
func Validate(cfg BackupConfig) []ValidationError {
	var errs []ValidationError

	if len(cfg.Jobs) == 0 {
		errs = append(errs, ValidationError{Field: "jobs", Reason: "no jobs defined"})
		return errs
	}

	names := make(map[string]bool)
	for _, job := range cfg.Jobs {
		errs = append(errs, validateJob(job, names)...)
	}
	return errs
}

func validateJob(job JobConfig, names map[string]bool) []ValidationError {
	var errs []ValidationError

	add := func(field, reason string) {
		errs = append(errs, ValidationError{Job: job.Name, Field: field, Reason: reason})
	}

	if job.Name == "" {
		add("name", "required")
	} else if names[job.Name] {
		add("name", "duplicate job name")
	} else {
		names[job.Name] = true
	}

	if job.Driver == "" {
		add("driver", "required")
	} else if job.Driver != "postgres" {
		add("driver", fmt.Sprintf("%q is not supported yet (only postgres)", job.Driver))
	}

	if job.Tool != "" && job.Tool != "wal-g" && job.Tool != "barman" {
		add("tool", fmt.Sprintf("%q is not a valid tool (wal-g or barman)", job.Tool))
	}

	if job.Environment.Type == "" {
		add("environment.type", "required")
	}
	if job.Environment.Namespace == "" {
		add("environment.namespace", "required")
	}

	if job.Schedule == "" {
		add("schedule", "required")
	}

	if job.Retention == "" {
		add("retention", "required")
	}

	if job.Destination.Type == "" {
		add("destination.type", "required")
	}
	if job.Destination.Bucket == "" {
		add("destination.bucket", "required")
	}

	if job.Encrypt {
		if job.Credentials.AgePublicKey == "" {
			add("credentials.age_public_key", "required when encrypt is true")
		} else if !strings.HasPrefix(job.Credentials.AgePublicKey, "age1") {
			add("credentials.age_public_key", "invalid format (must start with age1)")
		}
	}

	if job.Credentials.DBPassword.From == "" {
		add("credentials.db_password.from", "required")
	}
	if job.Credentials.S3AccessKey.From == "" {
		add("credentials.s3_access_key.from", "required")
	}
	if job.Credentials.S3SecretKey.From == "" {
		add("credentials.s3_secret_key.from", "required")
	}

	return errs
}
