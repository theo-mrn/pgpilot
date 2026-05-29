package k8s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

const ReplicationUser = "pgpilot"

var ErrInsufficientPrivilege = fmt.Errorf("insufficient privilege")

// EnsureReplicationUser creates the pgpilot replication user via a K8s Job.
// Returns (generatedPassword, alreadyExisted, err).
// If the DB user lacks privileges, returns ErrInsufficientPrivilege so the caller
// can display the SQL to run manually.
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
		context.Background(), ReplicationSecret, metav1.GetOptions{},
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

	k8sJob := buildJob(JobSpec{
		BaseName:  "repl-setup",
		Trigger:   "repl-setup",
		Namespace: job.Environment.Namespace,
		Image:     jobImage(job.DBVersion),
		Command:   script,
		Env:       pgEnvVars(job),
		TTL:       ttl(300),
	})
	// repl-setup jobs don't need the job name in labels
	k8sJob.Labels = map[string]string{ManagedByLabel: ManagedByValue}
	k8sJob.Spec.Template.Labels = nil

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

	// Store secret before returning so password is always consistent.
	if storeErr := upsertSecret(client, managedSecret(
		job.Environment.Namespace, ReplicationSecret,
		map[string]string{"username": ReplicationUser, "password": generatedPassword},
	)); storeErr != nil {
		return generatedPassword, false, fmt.Errorf("storing replication secret: %w", storeErr)
	}

	if exitCode == 2 {
		return generatedPassword, false, ErrInsufficientPrivilege
	}
	if exitCode != 0 {
		return generatedPassword, false, fmt.Errorf("setup job failed with exit code %d", exitCode)
	}
	return generatedPassword, false, nil
}

// VerifyReplicationUser checks that the pgpilot user exists with REPLICATION privilege.
func VerifyReplicationUser(kubeconfig string, job config.JobConfig) error {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	script := fmt.Sprintf(`
COUNT=$(psql --no-password -tAc "SELECT COUNT(*) FROM pg_roles WHERE rolname='%s' AND rolreplication")
if [ "$COUNT" = "0" ]; then
  echo "Replication user not found"
  exit 1
fi
echo "Replication user verified"
`, ReplicationUser)

	k8sJob := buildJob(JobSpec{
		BaseName:  "repl-verify",
		Trigger:   "repl-verify",
		Namespace: job.Environment.Namespace,
		Image:     jobImage(job.DBVersion),
		Command:   script,
		Env:       pgEnvVars(job),
		TTL:       ttl(300),
	})
	k8sJob.Labels = map[string]string{ManagedByLabel: ManagedByValue}

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
	return upsertSecret(client, managedSecret(
		namespace, ReplicationSecret,
		map[string]string{"username": ReplicationUser, "password": password},
	))
}

// TriggerBaseBackup creates a Job that runs pg_basebackup and uploads to S3.
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

	replPassword, err := readSecretByKey(client, job.Environment.Namespace, ReplicationSecret, "password")
	if err != nil {
		return nil, fmt.Errorf("reading replication secret (run 'dbpilot pitr enable' first): %w", err)
	}
	s3AccessKey, err := readSecret(client, dest.S3AccessKey.From)
	if err != nil {
		return nil, fmt.Errorf("reading S3 access key: %w", err)
	}
	s3SecretKey, err := readSecret(client, dest.S3SecretKey.From)
	if err != nil {
		return nil, fmt.Errorf("reading S3 secret key: %w", err)
	}

	region := defaultRegion(dest.Region)
	baseKey := fmt.Sprintf("s3://%s/%s/basebackup/base.tar.gz", dest.Bucket, dest.Prefix)
	endpointFlag := ""
	if dest.Endpoint != "" {
		endpointFlag = "--endpoint-url " + dest.Endpoint
	}
	dbname := job.Credentials.DBNameValue
	if dbname == "" {
		dbname = "postgres"
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
		defaultPort(job.Credentials.DBPort),
		ReplicationUser,
		replPassword,
		s3AccessKey,
		s3SecretKey,
		region,
		func() string {
			if dest.Endpoint != "" {
				return "export AWS_ENDPOINT='" + dest.Endpoint + "'"
			}
			return ""
		}(),
		baseKey, region, endpointFlag,
		baseKey,
	)

	k8sJob := buildJob(JobSpec{
		BaseName:  job.Name,
		Trigger:   "basebackup",
		Namespace: job.Environment.Namespace,
		Image:     WALImage,
		Command:   script,
		TTL:       ttl(3600),
	})

	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(
		context.Background(), k8sJob, metav1.CreateOptions{},
	)
	if err != nil {
		return nil, err
	}
	return created, nil
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
