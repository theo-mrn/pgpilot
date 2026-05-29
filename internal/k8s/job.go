package k8s

import (
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// JobSpec holds the parameters for building a one-off K8s Job.
type JobSpec struct {
	// BaseName is the logical name (e.g. job.Name). The final job name is
	// derived as "dbpilot-<BaseName>-<trigger>-<timestamp>", truncated to 63 chars
	// while always preserving the timestamp suffix.
	BaseName  string
	Trigger   string
	Namespace string
	Image     string
	Command   string
	Env       []corev1.EnvVar
	TTL       *int32 // seconds after finished; nil = no TTL
}

// buildJob creates a batchv1.Job from a JobSpec.
func buildJob(spec JobSpec) *batchv1.Job {
	ts := time.Now().UTC().Format("20060102-150405")
	suffix := fmt.Sprintf("-%s-%s", spec.Trigger, ts)
	prefix := "dbpilot-" + spec.BaseName
	if len(prefix)+len(suffix) > 63 {
		prefix = prefix[:63-len(suffix)]
	}
	name := prefix + suffix

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: spec.Namespace,
			Labels: map[string]string{
				ManagedByLabel:    ManagedByValue,
				"dbpilot/job":     spec.BaseName,
				"dbpilot/trigger": spec.Trigger,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: spec.TTL,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            spec.Trigger,
							Image:           spec.Image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/bin/sh", "-c", spec.Command},
							Env:             spec.Env,
						},
					},
				},
			},
		},
	}
}

func ttl(seconds int32) *int32 { return &seconds }
