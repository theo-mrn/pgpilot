package k8s

import (
	"context"
	"fmt"
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
	if job.Credentials.DBName.From != "" {
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

	ts := fmt.Sprintf("%d", time.Now().UnixNano()%1e9/1e6) // ms suffix
	ts = time.Now().UTC().Format("20060102-150405") + "-" + ts
	jobName := fmt.Sprintf("dbpilot-%s-restore-%s", job.Name, ts)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

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
