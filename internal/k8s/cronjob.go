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
	Action    string // "created", "updated", or "dry-run"
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

// TriggerBackup creates a one-off Job from the CronJob template for the given job name.
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

	ts := time.Now().UTC().Format("20060102-150405")
	suffix := "-manual-" + ts
	prefix := "dbpilot-" + job.Name
	if len(prefix)+len(suffix) > 63 {
		prefix = prefix[:63-len(suffix)]
	}

	cj := buildCronJob(*job)
	manualJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prefix + suffix,
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
	successfulJobsLimit := int32(3)
	failedJobsLimit := int32(3)
	envVars, script := buildBackupEnvVarsAndScript(job)

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dbpilot-" + job.Name,
			Namespace: job.Environment.Namespace,
			Labels: map[string]string{
				ManagedByLabel:  ManagedByValue,
				"dbpilot/job":   job.Name,
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
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{
								{
									Name:            "backup",
									Image:           jobImage(job.DBVersion),
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

// buildBackupEnvVarsAndScript builds env vars and shell script for a backup job.
// Each destination gets prefixed AWS_* env vars and its own upload command.
func buildBackupEnvVarsAndScript(job config.JobConfig) ([]corev1.EnvVar, string) {
	envVars := pgEnvVars(job)

	script := "set -e\nTMPFILE=$(mktemp)\ntrap 'rm -f $TMPFILE' EXIT\necho 'Dumping...'\npg_dump --no-password -Fc > $TMPFILE\necho 'Verifying dump integrity...'\npg_restore --list $TMPFILE > /dev/null\necho 'Dump verified.'\n"

	for i, dest := range job.Destinations {
		prefix := fmt.Sprintf("DEST%d_", i)
		accessKeyVar := prefix + "AWS_ACCESS_KEY_ID"
		secretKeyVar := prefix + "AWS_SECRET_ACCESS_KEY"
		regionVar := prefix + "AWS_DEFAULT_REGION"

		envVars = append(envVars,
			corev1.EnvVar{
				Name:      accessKeyVar,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3AccessKey.From)},
			},
			corev1.EnvVar{
				Name:      secretKeyVar,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(dest.S3SecretKey.From)},
			},
			corev1.EnvVar{Name: regionVar, Value: defaultRegion(dest.Region)},
		)

		s3Key := fmt.Sprintf("s3://%s/%s/$(date -u +%%Y%%m%%dT%%H%%M%%SZ).dump.gz", dest.Bucket, dest.Prefix)
		uploadCmd := fmt.Sprintf(
			"AWS_ACCESS_KEY_ID=$%s AWS_SECRET_ACCESS_KEY=$%s AWS_DEFAULT_REGION=$%s aws s3 cp $TMPFILE %s",
			accessKeyVar, secretKeyVar, regionVar, s3Key,
		)
		if dest.Endpoint != "" {
			uploadCmd += " --endpoint-url " + dest.Endpoint
		}
		script += uploadCmd + "\n"
	}

	return envVars, script
}

// pgEnvVars returns the standard Postgres env vars for a job using SecretKeyRef where possible.
func pgEnvVars(job config.JobConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(job.Credentials.DBPassword.From)},
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
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(job.Credentials.DBUser.From)},
		})
	}
	if job.Credentials.DBNameValue != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "PGDATABASE", Value: job.Credentials.DBNameValue})
	} else if job.Credentials.DBName.From != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: secretKeySelector(job.Credentials.DBName.From)},
		})
	}
	return envVars
}
