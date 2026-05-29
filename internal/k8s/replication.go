package k8s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

const ReplicationUser = "pgpilot"
const replicationSecretName = "dbpilot-replication-credentials"

var ErrInsufficientPrivilege = fmt.Errorf("insufficient privilege")

// TriggerBaseBackup creates a Job that runs pg_basebackup + wal-g backup-push.
// This produces a WAL-G compatible basebackup usable for PITR.
func TriggerBaseBackup(kubeconfig string, job config.JobConfig) (*batchv1.Job, error) {
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
	// Read replication credentials from the secret created by pitr enable.
	replSecret, err := client.CoreV1().Secrets(job.Environment.Namespace).Get(
		context.Background(), replicationSecretName, metav1.GetOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("reading replication secret (run 'dbpilot pitr enable' first): %w", err)
	}
	replPassword := string(replSecret.Data["password"])

	s3AccessKey, err := readSecretValueDirect(client,
		parseSecretRefNamespace(dest.S3AccessKey.From),
		parseSecretRefName(dest.S3AccessKey.From),
		parseSecretRefKey(dest.S3AccessKey.From),
	)
	if err != nil {
		return nil, fmt.Errorf("reading S3 access key: %w", err)
	}
	s3SecretKey, err := readSecretValueDirect(client,
		parseSecretRefNamespace(dest.S3SecretKey.From),
		parseSecretRefName(dest.S3SecretKey.From),
		parseSecretRefKey(dest.S3SecretKey.From),
	)
	if err != nil {
		return nil, fmt.Errorf("reading S3 secret key: %w", err)
	}

	endpointEnv := ""
	if dest.Endpoint != "" {
		endpointEnv = fmt.Sprintf("export AWS_ENDPOINT='%s'", dest.Endpoint)
	}

	dbname := job.Credentials.DBNameValue
	if dbname == "" {
		dbname = "postgres"
	}

	// Use pg_basebackup --format=tar piped directly to S3.
	// No WAL-G backup-push needed — avoids pg_backup_start permission issues.
	baseKey := fmt.Sprintf("s3://%s/%s/basebackup/base.tar.gz", dest.Bucket, dest.Prefix)
	endpointFlag := ""
	if dest.Endpoint != "" {
		endpointFlag = "--endpoint-url " + dest.Endpoint
	}

	script := fmt.Sprintf(`set -e
export PGHOST='%s'
export PGPORT='%s'
export PGUSER='%s'
export PGPASSWORD='%s'
export AWS_ACCESS_KEY_ID='%s'
export AWS_SECRET_ACCESS_KEY='%s'
export AWS_DEFAULT_REGION='%s'
%s

BASEDIR=$(mktemp -d)
trap 'rm -rf "$BASEDIR"' EXIT

echo "Running pg_basebackup..."
pg_basebackup \
  --no-password \
  --pgdata="$BASEDIR" \
  --format=plain \
  --wal-method=fetch \
  --checkpoint=fast \
  --progress

echo "Uploading to S3..."
tar -czf - -C "$BASEDIR" . | aws s3 cp - '%s' --region '%s' %s

echo "Basebackup complete: %s"
`,
		job.Credentials.DBHost,
		port(job),
		ReplicationUser,
		replPassword,
		s3AccessKey,
		s3SecretKey,
		region,
		endpointEnv,
		baseKey, region, endpointFlag,
		baseKey,
	)

	ts := time.Now().UTC().Format("20060102-150405")
	suffix := "-basebackup-" + ts
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
				"dbpilot/trigger":              "basebackup",
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: func() *int32 { v := int32(3600); return &v }(),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "basebackup",
							Image:           walImageDefault,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/sh", "-c", script},
						},
					},
				},
			},
		},
	}

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(
		context.Background(), k8sJob, metav1.CreateOptions{},
	)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func port(job config.JobConfig) string {
	if job.Credentials.DBPort != 0 {
		return fmt.Sprintf("%d", job.Credentials.DBPort)
	}
	return "5432"
}

func parseSecretRefNamespace(ref string) string {
	r := stripPrefix(ref, "k8s-secret://")
	slashIdx := indexOf(r, '/')
	if slashIdx < 0 {
		return "default"
	}
	return r[:slashIdx]
}

func parseSecretRefName(ref string) string {
	r := stripPrefix(ref, "k8s-secret://")
	hashIdx := indexOf(r, '#')
	if hashIdx >= 0 {
		r = r[:hashIdx]
	}
	slashIdx := indexOf(r, '/')
	if slashIdx < 0 {
		return r
	}
	return r[slashIdx+1:]
}

func parseSecretRefKey(ref string) string {
	hashIdx := indexOf(ref, '#')
	if hashIdx < 0 {
		return ""
	}
	return ref[hashIdx+1:]
}


// EnsureReplicationUser tries to create the pgpilot replication user via a K8s Job.
// Returns (generatedPassword, alreadyExisted, err).
// If the DB user lacks privileges, returns ErrInsufficientPrivilege + the password
// so the caller can display the SQL to run manually.
func EnsureReplicationUser(kubeconfig string, job config.JobConfig) (password string, alreadyExisted bool, err error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "", false, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return "", false, fmt.Errorf("creating kubernetes client: %w", err)
	}

	// Secret already exists — user was created in a previous run.
	_, err = client.CoreV1().Secrets(job.Environment.Namespace).Get(
		context.Background(), replicationSecretName, metav1.GetOptions{},
	)
	if err == nil {
		return "", true, nil
	}
	if !errors.IsNotFound(err) {
		return "", false, fmt.Errorf("checking replication secret: %w", err)
	}

	generatedPassword, err := generatePassword()
	if err != nil {
		return "", false, err
	}

	image := backupImageForJob(job)
	script := fmt.Sprintf(`
OUTPUT=$(psql --no-password -tAc "SELECT 1 FROM pg_roles WHERE rolname='%s' AND rolreplication" 2>&1)
if [ "$OUTPUT" = "1" ]; then
  echo "Replication user already exists"
  exit 0
fi
RESULT=$(psql --no-password -c "CREATE USER %s WITH REPLICATION LOGIN PASSWORD '%s'" 2>&1)
RC=$?
echo "$RESULT"
if echo "$RESULT" | grep -qi "insufficient_privilege\|permission denied\|must be superuser\|only roles with"; then
  exit 2
fi
exit $RC
`, ReplicationUser, ReplicationUser, generatedPassword)

	envVars := buildPGEnvVars(job)
	ts := time.Now().UTC().Format("20060102-150405")
	jobName := fmt.Sprintf("dbpilot-repl-setup-%s", ts)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	ttl := int32(300)

	k8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: job.Environment.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "repl-setup",
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

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(
		context.Background(), k8sJob, metav1.CreateOptions{},
	)
	if err != nil {
		return generatedPassword, false, fmt.Errorf("creating setup job: %w", err)
	}

	exitCode, err := waitForJobExitCode(client, created, 2*60*time.Second)
	if err != nil {
		return generatedPassword, false, fmt.Errorf("waiting for setup job: %w", err)
	}

	// Store the secret before returning so the password is always consistent,
	// whether creation succeeded automatically or needs manual intervention.
	if err := storeReplicationSecret(client, job.Environment.Namespace, generatedPassword); err != nil {
		return generatedPassword, false, fmt.Errorf("storing replication secret: %w", err)
	}

	if exitCode == 2 {
		return generatedPassword, false, ErrInsufficientPrivilege
	}
	if exitCode != 0 {
		return generatedPassword, false, fmt.Errorf("setup job failed with exit code %d", exitCode)
	}

	return generatedPassword, false, nil
}

// VerifyReplicationUser checks that the pgpilot user exists with REPLICATION via a K8s Job.
func VerifyReplicationUser(kubeconfig string, job config.JobConfig) error {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	image := backupImageForJob(job)
	script := fmt.Sprintf(`
COUNT=$(psql --no-password -tAc "SELECT COUNT(*) FROM pg_roles WHERE rolname='%s' AND rolreplication")
if [ "$COUNT" = "0" ]; then
  echo "Replication user not found"
  exit 1
fi
echo "Replication user verified"
`, ReplicationUser)

	envVars := buildPGEnvVars(job)
	ts := time.Now().UTC().Format("20060102-150405")
	jobName := fmt.Sprintf("dbpilot-repl-verify-%s", ts)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	ttl := int32(300)

	k8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: job.Environment.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "repl-verify",
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

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(
		context.Background(), k8sJob, metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("creating verify job: %w", err)
	}

	exitCode, err := waitForJobExitCode(client, created, 2*60*time.Second)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("replication user %q not found or missing REPLICATION privilege", ReplicationUser)
	}
	return nil
}

// StoreReplicationPassword stores a manually provided password in the K8s Secret.
func StoreReplicationPassword(kubeconfig, namespace, password string) error {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return err
	}
	return storeReplicationSecret(client, namespace, password)
}

func storeReplicationSecret(client kubernetes.Interface, namespace, password string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicationSecretName,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		StringData: map[string]string{
			"username": ReplicationUser,
			"password": password,
		},
	}
	_, err := client.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	}
	return err
}

// buildPGEnvVars returns the Postgres env vars for a job.
func buildPGEnvVars(job config.JobConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From)},
		},
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
	return envVars
}

// waitForJobExitCode polls until the Job succeeds or fails, then returns the exit code.
func waitForJobExitCode(client kubernetes.Interface, job *batchv1.Job, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		j, err := client.BatchV1().Jobs(job.Namespace).Get(
			context.Background(), job.Name, metav1.GetOptions{},
		)
		if err != nil {
			return -1, err
		}
		if j.Status.Succeeded > 0 {
			return 0, nil
		}
		if j.Status.Failed > 0 {
			pods, err := client.CoreV1().Pods(job.Namespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
			})
			if err == nil {
				for _, pod := range pods.Items {
					for _, cs := range pod.Status.ContainerStatuses {
						if cs.State.Terminated != nil {
							return int(cs.State.Terminated.ExitCode), nil
						}
					}
				}
			}
			return 1, nil
		}
		time.Sleep(3 * time.Second)
	}
	return -1, fmt.Errorf("timeout waiting for job %q", job.Name)
}

func generatePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
