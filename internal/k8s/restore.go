package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	image := backupImageForJob(job)

	envVars := []corev1.EnvVar{
		{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From)},
		},
		{
			Name:      "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(dest.S3AccessKey.From)},
		},
		{
			Name:      "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(dest.S3SecretKey.From)},
		},
	}
	region := dest.Region
	if region == "" {
		region = "us-east-1"
	}
	envVars = append(envVars, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: region})

	if job.Credentials.DBHost != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGHOST", Value: job.Credentials.DBHost})
	}
	if job.Credentials.DBPort != 0 {
		envVars = append(envVars, corev1.EnvVar{Name: "PGPORT", Value: fmt.Sprintf("%d", job.Credentials.DBPort)})
	}
	if job.Credentials.DBUser.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGUSER",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBUser.From)},
		})
	}
	if job.Credentials.DBNameValue != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGDATABASE", Value: job.Credentials.DBNameValue})
	} else if job.Credentials.DBName.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBName.From)},
		})
	}

	downloadCmd := fmt.Sprintf("aws s3 cp %s $TMPFILE", s3URL)
	if dest.Endpoint != "" {
		downloadCmd += fmt.Sprintf(" --endpoint-url %s", dest.Endpoint)
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

	ts := time.Now().UTC().Format("20060102-150405")
	restoreSuffix := "-restore-" + ts
	restorePrefix := fmt.Sprintf("dbpilot-%s", job.Name)
	if len(restorePrefix)+len(restoreSuffix) > 63 {
		restorePrefix = restorePrefix[:63-len(restoreSuffix)]
	}
	jobName := restorePrefix + restoreSuffix

	restartPolicy := corev1.RestartPolicyNever
	k8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: job.Environment.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/job":                  job.Name,
				"dbpilot/trigger":              "restore",
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: restartPolicy,
					Containers: []corev1.Container{
						{
							Name:            "restore",
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/sh", "-c", script},
							Env:             envVars,
						},
					},
				},
			},
		},
	}

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(context.Background(), k8sJob, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// TriggerPITRRestore creates a Job that uses WAL-G to restore to a specific point in time.
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
	region := dest.Region
	if region == "" {
		region = "us-east-1"
	}

	walgPrefix := fmt.Sprintf("s3://%s/%s/wal", dest.Bucket, dest.Prefix)

	envVars := []corev1.EnvVar{
		{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From)},
		},
		{
			Name:      "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(dest.S3AccessKey.From)},
		},
		{
			Name:      "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(dest.S3SecretKey.From)},
		},
		{Name: "AWS_DEFAULT_REGION", Value: region},
		{Name: "WALG_S3_PREFIX", Value: walgPrefix},
		{Name: "WALG_COMPRESSION_METHOD", Value: "lz4"},
		{Name: "PITR_TARGET_TIME", Value: targetTime},
	}
	if dest.Endpoint != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_ENDPOINT", Value: dest.Endpoint})
	}
	if job.Credentials.DBHost != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGHOST", Value: job.Credentials.DBHost})
	}
	if job.Credentials.DBPort != 0 {
		envVars = append(envVars, corev1.EnvVar{Name: "PGPORT", Value: fmt.Sprintf("%d", job.Credentials.DBPort)})
	}
	if job.Credentials.DBUser.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGUSER",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBUser.From)},
		})
	}
	if job.Credentials.DBNameValue != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGDATABASE", Value: job.Credentials.DBNameValue})
	} else if job.Credentials.DBName.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBName.From)},
		})
	}

	// PITR: fetch basebackup tar from S3, replay WALs via wal-g, dump to S3.
	baseKey := fmt.Sprintf("s3://%s/%s/basebackup/base.tar.gz", dest.Bucket, dest.Prefix)
	walgPrefix = fmt.Sprintf("s3://%s/%s/wal", dest.Bucket, dest.Prefix)
	dumpS3Key := fmt.Sprintf("s3://%s/%s/pitr-%s.dump.gz",
		dest.Bucket,
		dest.Prefix,
		strings.ReplaceAll(targetTime, ":", "-"),
	)
	endpointFlag := ""
	if dest.Endpoint != "" {
		endpointFlag = "--endpoint-url " + dest.Endpoint
	}
	script := fmt.Sprintf(`set -e
export WALG_S3_PREFIX='%s'
export WALG_COMPRESSION_METHOD=lz4

PGDATA=$(mktemp -d)
SOCK_DIR=$(mktemp -d)
trap 'pg_ctl -D "$PGDATA" stop -m immediate 2>/dev/null || true; rm -rf "$PGDATA" "$SOCK_DIR"' EXIT

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

# Replace pg_hba.conf to allow local connections without CNPG usermaps
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
`, walgPrefix, baseKey, endpointFlag, targetTime, targetTime, dumpS3Key, endpointFlag, dumpS3Key)

	ts := time.Now().UTC().Format("20060102-150405")
	suffix := "-pitr-" + ts
	prefix := fmt.Sprintf("dbpilot-%s", job.Name)
	if len(prefix)+len(suffix) > 63 {
		prefix = prefix[:63-len(suffix)]
	}
	jobName := prefix + suffix

	k8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: job.Environment.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/job":                  job.Name,
				"dbpilot/trigger":              "pitr-restore",
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "pitr-restore",
							Image:           walImageDefault,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/sh", "-c", script},
							Env:             envVars,
						},
					},
				},
			},
		},
	}

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(context.Background(), k8sJob, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return created, nil
}
