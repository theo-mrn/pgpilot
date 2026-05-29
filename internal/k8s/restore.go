package k8s

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

// TriggerRestore creates a one-off Job that downloads a backup from S3 and restores it.
func TriggerRestore(kubeconfig string, job config.JobConfig, s3URL string, destIndex int) (*batchv1.Job, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	dest := job.Destinations[destIndex]

	envVars := pgEnvVars(job)
	envVars = append(envVars,
		corev1.EnvVar{
			Name:      "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3AccessKey.From)},
		},
		corev1.EnvVar{
			Name:      "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3SecretKey.From)},
		},
		corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: defaultRegion(dest.Region)},
	)

	downloadCmd := fmt.Sprintf("aws s3 cp %s $TMPFILE", s3URL)
	if dest.Endpoint != "" {
		downloadCmd += " --endpoint-url " + dest.Endpoint
	}

	script := fmt.Sprintf(`set -e
TMPFILE=$(mktemp)
trap 'rm -f $TMPFILE' EXIT
echo "Downloading backup from %s..."
%s
echo "Restoring..."
pg_restore --no-password -Fc --clean --if-exists -d $PGDATABASE $TMPFILE
echo "Done."
`, s3URL, downloadCmd)

	k8sJob := buildJob(JobSpec{
		BaseName:  job.Name,
		Trigger:   "restore",
		Namespace: job.Environment.Namespace,
		Image:     jobImage(job.DBVersion),
		Command:   script,
		Env:       envVars,
		TTL:       ttl(300),
	})

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(context.Background(), k8sJob, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// TriggerPITRRestore creates a Job that fetches a basebackup from S3, replays WALs
// to a target time, and dumps the result back to S3.
func TriggerPITRRestore(kubeconfig string, job config.JobConfig, targetTime string) (*batchv1.Job, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	if len(job.Destinations) == 0 {
		return nil, fmt.Errorf("no destinations configured for job %q", job.Name)
	}
	dest := job.Destinations[0]
	region := defaultRegion(dest.Region)
	walgPrefix := fmt.Sprintf("s3://%s/%s/wal", dest.Bucket, dest.Prefix)
	baseKey := fmt.Sprintf("s3://%s/%s/basebackup/base.tar.gz", dest.Bucket, dest.Prefix)
	dumpKey := fmt.Sprintf("s3://%s/%s/pitr-%s.dump.gz",
		dest.Bucket, dest.Prefix,
		strings.ReplaceAll(targetTime, ":", "-"),
	)
	endpointFlag := ""
	if dest.Endpoint != "" {
		endpointFlag = "--endpoint-url " + dest.Endpoint
	}

	envVars := pgEnvVars(job)
	envVars = append(envVars,
		corev1.EnvVar{
			Name:      "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3AccessKey.From)},
		},
		corev1.EnvVar{
			Name:      "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3SecretKey.From)},
		},
		corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: region},
		corev1.EnvVar{Name: "WALG_S3_PREFIX", Value: walgPrefix},
		corev1.EnvVar{Name: "WALG_COMPRESSION_METHOD", Value: "lz4"},
	)
	if dest.Endpoint != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_ENDPOINT", Value: dest.Endpoint})
	}

	script := fmt.Sprintf(`set -e
PGDATA=$(mktemp -d)
SOCK_DIR=$(mktemp -d)
trap 'PATH=/usr/lib/postgresql/16/bin:$PATH pg_ctl -D "$PGDATA" stop -m immediate 2>/dev/null || true; rm -rf "$PGDATA" "$SOCK_DIR"' EXIT

echo "Fetching base backup from S3..."
aws s3 cp '%s' - --region "$AWS_DEFAULT_REGION" %s | tar -xz -C "$PGDATA"

echo "Configuring recovery to: %s"
cat > "$PGDATA/postgresql.conf" <<EOF
restore_command = 'wal-g wal-fetch %%f %%p'
recovery_target_time = '%s'
recovery_target_action = promote
recovery_target_inclusive = true
unix_socket_directories = '$SOCK_DIR'
ssl = off
listen_addresses = ''
max_worker_processes = 64
max_parallel_workers = 64
EOF

cat > "$PGDATA/pg_hba.conf" <<EOF
local all all trust
EOF

touch "$PGDATA/recovery.signal"

export PATH="/usr/lib/postgresql/16/bin:$PATH"

echo "Starting Postgres in recovery mode..."
chown -R postgres:postgres "$PGDATA" "$SOCK_DIR"
su postgres -c "PATH=/usr/lib/postgresql/16/bin:$PATH pg_ctl -D '$PGDATA' -w -t 300 start"

echo "Waiting for recovery to complete..."
until su postgres -c "PATH=/usr/lib/postgresql/16/bin:$PATH pg_isready -h '$SOCK_DIR'" 2>/dev/null; do sleep 2; done

echo "Dumping database to S3..."
su postgres -c "PATH=/usr/lib/postgresql/16/bin:$PATH pg_dump -Fc -U postgres -h '$SOCK_DIR' -d '$PGDATABASE'" | \
  aws s3 cp - '%s' --region "$AWS_DEFAULT_REGION" %s

echo "Done. Dump available at: %s"
`, baseKey, endpointFlag, targetTime, targetTime, dumpKey, endpointFlag, dumpKey)

	k8sJob := buildJob(JobSpec{
		BaseName:  job.Name,
		Trigger:   "pitr",
		Namespace: job.Environment.Namespace,
		Image:     WALImage,
		Command:   script,
		Env:       envVars,
		TTL:       ttl(300),
	})

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(context.Background(), k8sJob, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return created, nil
}
