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

// WALDeployResult describes what happened to a WAL Deployment.
type WALDeployResult struct {
	Namespace string
	Action    string // "created", "updated", "deleted", "not-found", "dry-run"
	JobCount  int
}

// WALAgentStatus returns the status of the WAL Deployment in each namespace.
type WALAgentStatus struct {
	Namespace string
	Ready     int32
	Desired   int32
	Message   string
}

// DeployWALAgents deploys one WAL streaming Deployment per namespace for all
// jobs that have pitr.enabled = true.
func DeployWALAgents(kubeconfig string, cfg config.BackupConfig, dryRun bool) ([]WALDeployResult, error) {
	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	byNamespace := groupJobsByNamespace(cfg, true)

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
		err := client.AppsV1().Deployments(ns).Delete(context.Background(), WALDeploymentName, metav1.DeleteOptions{})
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

// GetWALAgentStatus returns the readiness of the WAL agent Deployment per namespace.
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

		dep, err := client.AppsV1().Deployments(ns).Get(context.Background(), WALDeploymentName, metav1.GetOptions{})
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

func buildWALDeployment(namespace string, jobs []config.JobConfig, client kubernetes.Interface) (*appsv1.Deployment, error) {
	streamConfigs, err := buildStreamConfigs(namespace, jobs, client)
	if err != nil {
		return nil, err
	}

	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WALDeploymentName,
			Namespace: namespace,
			Labels: map[string]string{
				ManagedByLabel:       ManagedByValue,
				"dbpilot/component":  "wal-agent",
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
						ManagedByLabel:      ManagedByValue,
						"dbpilot/component": "wal-agent",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:            "wal-agent",
							Image:           WALImage,
							ImagePullPolicy: corev1.PullAlways,
							Env:             []corev1.EnvVar{{Name: "STREAM_CONFIGS", Value: streamConfigs}},
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

// buildStreamConfigs encodes all jobs as a semicolon-separated STREAM_CONFIGS value.
// Format per entry: HOST|PORT|USER|PASSWORD|WALG_PREFIX|ACCESS_KEY|SECRET_KEY|REGION|ENDPOINT
func buildStreamConfigs(namespace string, jobs []config.JobConfig, client kubernetes.Interface) (string, error) {
	replUser, err := readSecretByKey(client, namespace, ReplicationSecret, "username")
	if err != nil {
		return "", fmt.Errorf("reading replication user: %w", err)
	}
	replPassword, err := readSecretByKey(client, namespace, ReplicationSecret, "password")
	if err != nil {
		return "", fmt.Errorf("reading replication password: %w", err)
	}

	entries := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if len(job.Destinations) == 0 {
			continue
		}
		dest := job.Destinations[0]

		accessKey, err := readSecret(client, dest.S3AccessKey.From)
		if err != nil {
			return "", fmt.Errorf("job %q s3_access_key: %w", job.Name, err)
		}
		secretKey, err := readSecret(client, dest.S3SecretKey.From)
		if err != nil {
			return "", fmt.Errorf("job %q s3_secret_key: %w", job.Name, err)
		}

		walgPrefix := fmt.Sprintf("s3://%s/%s/wal", dest.Bucket, dest.Prefix)
		entry := strings.Join([]string{
			job.Credentials.DBHost,
			defaultPort(job.Credentials.DBPort),
			replUser, replPassword,
			walgPrefix, accessKey, secretKey,
			defaultRegion(dest.Region),
			dest.Endpoint,
		}, "|")
		entries = append(entries, entry)
	}
	return strings.Join(entries, ";"), nil
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

// groupJobsByNamespace groups jobs by namespace, optionally filtering to pitr-enabled only.
func groupJobsByNamespace(cfg config.BackupConfig, pitrOnly bool) map[string][]config.JobConfig {
	result := map[string][]config.JobConfig{}
	for _, job := range cfg.Jobs {
		if pitrOnly && !job.PITR.Enabled {
			continue
		}
		result[job.Environment.Namespace] = append(result[job.Environment.Namespace], job)
	}
	return result
}
