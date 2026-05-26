package k8s

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

// DeployResult describes what happened to a single CronJob during deploy.
type DeployResult struct {
	JobName   string
	Namespace string
	Action    string // "created" or "updated"
}

// DeployBackupJobs creates or updates a CronJob for each job in cfg.
func DeployBackupJobs(kubeconfig string, cfg config.BackupConfig, dryRun bool) ([]DeployResult, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	var results []DeployResult
	for _, job := range cfg.Jobs {
		if !dryRun {
			if err := InjectWALGSidecar(client, job); err != nil {
				return results, fmt.Errorf("injecting WAL-G sidecar for %q: %w", job.Name, err)
			}
			if err := EnsureBackupRBAC(client, job.Environment.Namespace); err != nil {
				return results, fmt.Errorf("ensuring RBAC for %q: %w", job.Name, err)
			}
		}
		cj := buildCronJob(job)
		result, err := applyNamespacedCronJob(client, cj, job.Environment.Namespace, dryRun)
		if err != nil {
			return results, fmt.Errorf("job %q: %w", job.Name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func applyNamespacedCronJob(client kubernetes.Interface, cj *batchv1.CronJob, namespace string, dryRun bool) (DeployResult, error) {
	result := DeployResult{JobName: cj.Name, Namespace: namespace}

	if dryRun {
		result.Action = "dry-run"
		return result, nil
	}

	_, err := client.BatchV1().CronJobs(namespace).Get(context.Background(), cj.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = client.BatchV1().CronJobs(namespace).Create(context.Background(), cj, metav1.CreateOptions{})
		result.Action = "created"
	} else if err == nil {
		_, err = client.BatchV1().CronJobs(namespace).Update(context.Background(), cj, metav1.UpdateOptions{})
		result.Action = "updated"
	}
	if err != nil {
		return result, fmt.Errorf("applying cronjob: %w", err)
	}
	return result, nil
}

func buildCronJob(job config.JobConfig) *batchv1.CronJob {
	image := backupImageForJob(job)
	jobName := "dbpilot-" + job.Name
	successfulJobsLimit := int32(3)
	failedJobsLimit := int32(3)

	s3Prefix := fmt.Sprintf("s3://%s/%s/basebackup", job.Destination.Bucket, job.Destination.Prefix)

	envVars := []corev1.EnvVar{
		{Name: "WALG_S3_PREFIX", Value: s3Prefix},
		{Name: "PGDATA", Value: pgDataPath},
		{
			Name: "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From),
			},
		},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: parseSecretRef(job.Credentials.S3AccessKey.From),
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: parseSecretRef(job.Credentials.S3SecretKey.From),
			},
		},
	}

	if job.Destination.Region != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: job.Destination.Region})
	}
	if job.Destination.Endpoint != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_ENDPOINT_URL", Value: job.Destination.Endpoint})
	}
	if job.Credentials.DBHost != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGHOST", Value: job.Credentials.DBHost})
	}
	if job.Credentials.DBUser.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGUSER",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBUser.From)},
		})
	}
	if job.Credentials.DBName.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBName.From)},
		})
	}
	if job.Encrypt && job.Credentials.AgePublicKey != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "WALG_LIBSODIUM_KEY", Value: job.Credentials.AgePublicKey})
	}

	ns := job.Environment.Namespace
	deployName := job.Credentials.DBHost
	container := job.Environment.Container

	// Build export statements — env vars resolved in CronJob pod, written to a temp file then sourced in exec
	exports := ""
	for _, e := range envVars {
		exports += fmt.Sprintf("export %s\\n", e.Name)
	}

	script := fmt.Sprintf(`set -e
until kubectl get pods -n %s -l app=%s --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | grep -q .; do sleep 2; done
POD=$(kubectl get pods -n %s -l app=%s --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n %s "$POD" -c %s -- /bin/sh -c "$(printenv | grep -E '^(WALG_|AWS_|PG)' | sed 's/^/export /' | tr '\n' ';') export PGHOST=localhost; export PGSSLMODE=disable; /walg-bin/wal-g backup-push %s"`,
		ns, deployName, ns, deployName, ns, container, pgDataPath)

	restartPolicy := corev1.RestartPolicyOnFailure

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: job.Environment.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/job":                  job.Name,
			},
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   job.Schedule,
			SuccessfulJobsHistoryLimit: &successfulJobsLimit,
			FailedJobsHistoryLimit:     &failedJobsLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy:      restartPolicy,
							ServiceAccountName: BackupServiceAccount,
							Containers: []corev1.Container{
								{
									Name:            "backup",
									Image:           image,
									ImagePullPolicy: corev1.PullAlways,
									Command:         []string{"/bin/sh", "-c", script},
									Env:             envVars,
								},
							},
						},
					},
				},
			},
		},
	}
}

// backupImageForJob returns the backup image tag for the job's Postgres version.
func backupImageForJob(job config.JobConfig) string {
	version := job.DBVersion
	if version == "" {
		version = "16"
	}
	return fmt.Sprintf("maxwellfaraday/dbpilot-backup:pg%s", version)
}

// parseSecretRef parses a secret ref of the form:
// k8s-secret://namespace/secret-name#key
// and returns a SecretKeySelector.
func parseSecretRef(ref string) *corev1.SecretKeySelector {
	ref = stripPrefix(ref, "k8s-secret://")
	hashIdx := indexOf(ref, '#')
	key := ""
	if hashIdx >= 0 {
		key = ref[hashIdx+1:]
		ref = ref[:hashIdx]
	}
	slashIdx := indexOf(ref, '/')
	name := ref
	if slashIdx >= 0 {
		name = ref[slashIdx+1:]
	}
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}
}

func stripPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
