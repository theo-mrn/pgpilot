package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

const walDeploymentName = "dbpilot-wal-agent"
const walImageDefault = "maxwellfaraday/dbpilot-wal:latest"

// WALDeployResult describes what happened to a WAL Deployment.
type WALDeployResult struct {
	Namespace string
	Action    string // "created", "updated", "skipped"
	JobCount  int
}

// DeployWALAgents deploys one WAL streaming Deployment per namespace for all
// jobs that have pitr.enabled = true. Jobs in the same namespace are grouped
// into a single pod via the STREAM_CONFIGS env var.
func DeployWALAgents(kubeconfig string, cfg config.BackupConfig, dryRun bool) ([]WALDeployResult, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	// Group pitr-enabled jobs by namespace.
	byNamespace := map[string][]config.JobConfig{}
	for _, job := range cfg.Jobs {
		if job.PITR.Enabled {
			ns := job.Environment.Namespace
			byNamespace[ns] = append(byNamespace[ns], job)
		}
	}

	var results []WALDeployResult
	for ns, jobs := range byNamespace {
		deployment, err := buildWALDeployment(ns, jobs, client)
		if err != nil {
			return results, fmt.Errorf("namespace %q: %w", ns, err)
		}
		result := WALDeployResult{Namespace: ns, JobCount: len(jobs)}
		if dryRun {
			result.Action = "dry-run"
			results = append(results, result)
			continue
		}
		action, err := applyWALDeployment(client, ns, deployment)
		if err != nil {
			return results, fmt.Errorf("namespace %q: %w", ns, err)
		}
		result.Action = action
		results = append(results, result)
	}
	return results, nil
}

// RemoveWALAgents deletes the WAL Deployment in each namespace that has pitr jobs.
func RemoveWALAgents(kubeconfig string, cfg config.BackupConfig) ([]WALDeployResult, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	seen := map[string]bool{}
	var results []WALDeployResult
	for _, job := range cfg.Jobs {
		ns := job.Environment.Namespace
		if seen[ns] {
			continue
		}
		seen[ns] = true
		err := client.AppsV1().Deployments(ns).Delete(context.Background(), walDeploymentName, metav1.DeleteOptions{})
		if errors.IsNotFound(err) {
			results = append(results, WALDeployResult{Namespace: ns, Action: "not-found"})
			continue
		}
		if err != nil {
			return results, fmt.Errorf("namespace %q: %w", ns, err)
		}
		results = append(results, WALDeployResult{Namespace: ns, Action: "deleted"})
	}
	return results, nil
}

// WALAgentStatus returns the status of the WAL Deployment in each namespace.
type WALAgentStatus struct {
	Namespace string
	Ready     int32
	Desired   int32
	Message   string
}

func GetWALAgentStatus(kubeconfig string, cfg config.BackupConfig) ([]WALAgentStatus, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	seen := map[string]bool{}
	var statuses []WALAgentStatus
	for _, job := range cfg.Jobs {
		if !job.PITR.Enabled {
			continue
		}
		ns := job.Environment.Namespace
		if seen[ns] {
			continue
		}
		seen[ns] = true

		dep, err := client.AppsV1().Deployments(ns).Get(context.Background(), walDeploymentName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			statuses = append(statuses, WALAgentStatus{Namespace: ns, Message: "not deployed"})
			continue
		}
		if err != nil {
			return statuses, fmt.Errorf("namespace %q: %w", ns, err)
		}
		statuses = append(statuses, WALAgentStatus{
			Namespace: ns,
			Ready:     dep.Status.ReadyReplicas,
			Desired:   *dep.Spec.Replicas,
		})
	}
	return statuses, nil
}

// buildWALDeployment constructs the Deployment spec for a namespace.
// All jobs in the namespace are encoded in the STREAM_CONFIGS env var.
func buildWALDeployment(namespace string, jobs []config.JobConfig, client kubernetes.Interface) (*appsv1.Deployment, error) {
	envVars, err := buildStreamEnvVars(namespace, jobs, client)
	if err != nil {
		return nil, err
	}

	replicas := int32(1)
	image := walImageDefault

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      walDeploymentName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/component":            "wal-agent",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"dbpilot/component": "wal-agent"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "dbpilot",
						"dbpilot/component":            "wal-agent",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:            "wal-agent",
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Env:             envVars,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("64Mi"),
									corev1.ResourceCPU:    resource.MustParse("50m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("256Mi"),
									corev1.ResourceCPU:    resource.MustParse("200m"),
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

// buildStreamEnvVars resolves all secret refs and encodes them as STREAM_CONFIGS.
// Format: "HOST|PORT|USER|PASSWORD|WALG_PREFIX|ACCESS_KEY|SECRET_KEY|REGION|ENDPOINT"
// Multiple jobs are separated by semicolons.
func buildStreamEnvVars(namespace string, jobs []config.JobConfig, client kubernetes.Interface) ([]corev1.EnvVar, error) {
	// Read the pgpilot replication user credentials — same for all jobs in this namespace.
	replUser, err := readSecretValueDirect(client, namespace, replicationSecretName, "username")
	if err != nil {
		return nil, fmt.Errorf("reading replication user: %w", err)
	}
	replPassword, err := readSecretValueDirect(client, namespace, replicationSecretName, "password")
	if err != nil {
		return nil, fmt.Errorf("reading replication password: %w", err)
	}

	entries := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if len(job.Destinations) == 0 {
			continue
		}
		dest := job.Destinations[0]

		accessKey, err := readSecretValue(client, dest.S3AccessKey.From)
		if err != nil {
			return nil, fmt.Errorf("job %q s3_access_key: %w", job.Name, err)
		}
		secretKey, err := readSecretValue(client, dest.S3SecretKey.From)
		if err != nil {
			return nil, fmt.Errorf("job %q s3_secret_key: %w", job.Name, err)
		}

		host := job.Credentials.DBHost
		port := "5432"
		if job.Credentials.DBPort != 0 {
			port = fmt.Sprintf("%d", job.Credentials.DBPort)
		}
		region := dest.Region
		if region == "" {
			region = "us-east-1"
		}
		walgPrefix := fmt.Sprintf("s3://%s/%s/wal", dest.Bucket, dest.Prefix)

		entry := strings.Join([]string{
			host, port, replUser, replPassword,
			walgPrefix, accessKey, secretKey, region, dest.Endpoint,
		}, "|")
		entries = append(entries, entry)
	}

	return []corev1.EnvVar{
		{Name: "STREAM_CONFIGS", Value: strings.Join(entries, ";")},
	}, nil
}

// readSecretValueDirect reads a key directly from a secret by namespace/name/key.
func readSecretValueDirect(client kubernetes.Interface, namespace, secretName, key string) (string, error) {
	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading secret %s/%s: %w", namespace, secretName, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, secretName)
	}
	return string(val), nil
}

// readSecretValue resolves a k8s-secret:// ref to its plaintext value.
func readSecretValue(client kubernetes.Interface, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	// reuse existing parseSecretRef + read via API
	selector := parseSecretRef(ref)
	// extract namespace from ref: k8s-secret://namespace/name#key
	r := stripPrefix(ref, "k8s-secret://")
	slashIdx := indexOf(r, '/')
	ns := ""
	if slashIdx >= 0 {
		ns = r[:slashIdx]
	}
	secret, err := client.CoreV1().Secrets(ns).Get(context.Background(), selector.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	val, ok := secret.Data[selector.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", selector.Key, selector.Name)
	}
	return string(val), nil
}

func applyWALDeployment(client kubernetes.Interface, namespace string, dep *appsv1.Deployment) (string, error) {
	_, err := client.AppsV1().Deployments(namespace).Get(context.Background(), dep.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = client.AppsV1().Deployments(namespace).Create(context.Background(), dep, metav1.CreateOptions{})
		if err != nil {
			return "", err
		}
		return "created", nil
	}
	if err != nil {
		return "", err
	}
	_, err = client.AppsV1().Deployments(namespace).Update(context.Background(), dep, metav1.UpdateOptions{})
	if err != nil {
		return "", err
	}
	return "updated", nil
}
