package k8s

import (
	"context"
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

// DeployResult describes what happened to a single resource during deploy.
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

// TriggerBackup creates a one-off Job from the CronJob for the given job name.
func TriggerBackup(kubeconfig string, cfg config.BackupConfig, jobName string) (string, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return "", fmt.Errorf("creating kubernetes client: %w", err)
	}

	var job *config.JobConfig
	for i := range cfg.Jobs {
		if cfg.Jobs[i].Name == jobName {
			job = &cfg.Jobs[i]
			break
		}
	}
	if job == nil {
		return "", fmt.Errorf("job %q not found", jobName)
	}

	suffix := time.Now().UTC().Format("20060102-150405")
	manualName := fmt.Sprintf("dbpilot-%s-manual-%s", job.Name, suffix)
	if len(manualName) > 63 {
		manualName = manualName[:63]
	}

	cj := buildCronJob(*job)
	manualJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manualName,
			Namespace: job.Environment.Namespace,
			Labels:    cj.Labels,
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}
	created, err := client.BatchV1().Jobs(job.Environment.Namespace).Create(
		context.Background(), manualJob, metav1.CreateOptions{},
	)
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

func buildCronJob(job config.JobConfig) *batchv1.CronJob {
	image := backupImageForJob(job)
	jobName := "dbpilot-" + job.Name
	successfulJobsLimit := int32(3)
	failedJobsLimit := int32(3)

	envVars := buildEnvVars(job)

	// pg_dump streams a compressed SQL dump directly to S3 via aws s3 cp.
	// No modification to the Postgres pod or config required.
	s3Key := fmt.Sprintf("s3://%s/%s/$(date -u +%%Y%%m%%dT%%H%%M%%SZ).dump.gz", job.Destination.Bucket, job.Destination.Prefix)
	script := fmt.Sprintf(`set -e
pg_dump --no-password -Fc | aws s3 cp - %s`, s3Key)
	if job.Destination.Endpoint != "" {
		script += fmt.Sprintf(" --endpoint-url %s", job.Destination.Endpoint)
	}

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
							RestartPolicy: restartPolicy,
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

func buildEnvVars(job config.JobConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From)},
		},
		{
			Name:      "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.S3AccessKey.From)},
		},
		{
			Name:      "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.S3SecretKey.From)},
		},
	}
	if job.Destination.Region != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: job.Destination.Region})
	} else {
		// aws cli requires a region even for MinIO
		envVars = append(envVars, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: "us-east-1"})
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
	if job.Credentials.DBName.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBName.From)},
		})
	}
	return envVars
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
