package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/theomorin/dbpilot/internal/config"
)

const (
	pgDataPath      = "/var/lib/postgresql/data"
	walgBinVolume   = "dbpilot-walg-bin"
	walgConfigVolume = "dbpilot-wal-config"
	initContainerName = "dbpilot-install-walg"
)

// InjectWALGSidecar patches the Postgres Deployment/StatefulSet to:
// 1. Add an init container that copies wal-g into a shared emptyDir volume
// 2. Mount that volume + the WAL env config into the Postgres container
// 3. Create a ConfigMap with postgresql.conf overrides for WAL archiving
func InjectWALGSidecar(client kubernetes.Interface, job config.JobConfig) error {
	ns := job.Environment.Namespace
	// db_host matches the Deployment/StatefulSet name (K8s service name)
	deployName := job.Credentials.DBHost

	pgContainerName, volumeName, subPath, err := findPGDataVolume(client, ns, deployName)
	if err != nil {
		return err
	}

	return patchDeployment(client, ns, deployName, pgContainerName, volumeName, subPath, job)
}

// findPGDataVolume finds the container name and volume that holds pgdata in a Deployment or StatefulSet.
func findPGDataVolume(client kubernetes.Interface, namespace, deployName string) (pgContainerName, volumeName, subPath string, err error) {
	// Try Deployment first
	deploy, err := client.AppsV1().Deployments(namespace).Get(context.Background(), deployName, metav1.GetOptions{})
	if err == nil {
		for _, c := range deploy.Spec.Template.Spec.Containers {
			for _, vm := range c.VolumeMounts {
				if vm.MountPath == pgDataPath {
					return c.Name, vm.Name, vm.SubPath, nil
				}
			}
		}
		return "", "", "", fmt.Errorf("could not find pgdata volume mount in deployment %s", deployName)
	}

	// Try StatefulSet
	ss, err := client.AppsV1().StatefulSets(namespace).Get(context.Background(), deployName, metav1.GetOptions{})
	if err != nil {
		return "", "", "", fmt.Errorf("finding deployment or statefulset %q: %w", deployName, err)
	}
	for _, c := range ss.Spec.Template.Spec.Containers {
		for _, vm := range c.VolumeMounts {
			if vm.MountPath == pgDataPath {
				return c.Name, vm.Name, vm.SubPath, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("could not find pgdata volume mount in statefulset %s", deployName)
}


func patchDeployment(client kubernetes.Interface, namespace, deployName, pgContainerName, volumeName, subPath string, job config.JobConfig) error {
	deploy, err := client.AppsV1().Deployments(namespace).Get(context.Background(), deployName, metav1.GetOptions{})
	if err != nil {
		return patchStatefulSet(client, namespace, deployName, pgContainerName, volumeName, subPath, job)
	}

	podPatch := buildPatch(
		deploy.Spec.Template.Spec.InitContainers,
		deploy.Spec.Template.Spec.Containers,
		deploy.Spec.Template.Spec.Volumes,
		pgContainerName, volumeName, subPath, job,
	)
	recreate := appsv1.RecreateDeploymentStrategyType
	fullPatch := deployPatch{
		Spec: deployPatchSpec{
			Strategy: appsv1.DeploymentStrategy{Type: recreate},
			Template: podPatch.Spec.Template,
		},
	}
	patchBytes, err := json.Marshal(fullPatch)
	if err != nil {
		return err
	}
	_, err = client.AppsV1().Deployments(namespace).Patch(
		context.Background(), deployName,
		types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}

func patchStatefulSet(client kubernetes.Interface, namespace, name, pgContainerName, volumeName, subPath string, job config.JobConfig) error {
	ss, err := client.AppsV1().StatefulSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("finding deployment or statefulset %q: %w", name, err)
	}

	patch := buildPatch(
		ss.Spec.Template.Spec.InitContainers,
		ss.Spec.Template.Spec.Containers,
		ss.Spec.Template.Spec.Volumes,
		pgContainerName, volumeName, subPath, job,
	)
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = client.AppsV1().StatefulSets(namespace).Patch(
		context.Background(), name,
		types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}

type podSpecPatch struct {
	Spec podSpecPatchSpec `json:"spec"`
}

type deployPatch struct {
	Spec deployPatchSpec `json:"spec"`
}

type deployPatchSpec struct {
	Strategy appsv1.DeploymentStrategy `json:"strategy"`
	Template podSpecPatchTemplate      `json:"template"`
}

type podSpecPatchSpec struct {
	Template podSpecPatchTemplate `json:"template"`
}

type podSpecPatchTemplate struct {
	Spec podSpecPatchPodSpec `json:"spec"`
}

type podSpecPatchPodSpec struct {
	InitContainers []corev1.Container `json:"initContainers"`
	Containers     []corev1.Container `json:"containers"`
	Volumes        []corev1.Volume    `json:"volumes"`
}



func buildPatch(existingInits, existingContainers []corev1.Container, existingVols []corev1.Volume, pgContainerName, volumeName, subPath string, job config.JobConfig) podSpecPatch {
	// Remove any previously injected dbpilot entries to avoid duplicates
	cleanInits := make([]corev1.Container, 0)
	for _, c := range existingInits {
		if !strings.HasPrefix(c.Name, "dbpilot") {
			cleanInits = append(cleanInits, c)
		}
	}
	existingInits = cleanInits

	cleanVols := make([]corev1.Volume, 0)
	for _, v := range existingVols {
		if !strings.HasPrefix(v.Name, "dbpilot") {
			cleanVols = append(cleanVols, v)
		}
	}
	existingVols = cleanVols

	// Init container: copy wal-g binary into shared emptyDir
	initContainer := corev1.Container{
		Name:            initContainerName,
		Image:           backupImageForJob(job),
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{"/bin/sh", "-c",
			"cp /usr/local/bin/wal-g /walg-bin/wal-g && cp -r /etc/ssl/certs /walg-bin/certs",
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: walgBinVolume, MountPath: "/walg-bin"},
		},
	}

	// Add WAL-G env vars and volume mounts to the Postgres container
	s3Prefix := fmt.Sprintf("s3://%s/%s/wal", job.Destination.Bucket, job.Destination.Prefix)
	walgEnv := []corev1.EnvVar{
		{Name: "WALG_S3_PREFIX", Value: s3Prefix},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.S3AccessKey.From)},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.S3SecretKey.From)},
		},
	}
	if job.Destination.Region != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: job.Destination.Region})
	}
	if job.Destination.Endpoint != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{Name: "AWS_ENDPOINT_URL", Value: job.Destination.Endpoint})
	}
	if job.Encrypt && job.Credentials.AgePublicKey != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{Name: "WALG_LIBSODIUM_KEY", Value: job.Credentials.AgePublicKey})
	}
	if job.Credentials.DBPassword.From != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{
			Name:      "PGPASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBPassword.From)},
		})
	}
	if job.Credentials.DBUser.From != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{
			Name:      "PGUSER",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBUser.From)},
		})
	}
	if job.Credentials.DBName.From != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{
			Name:      "PGDATABASE",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: parseSecretRef(job.Credentials.DBName.From)},
		})
	}
	if job.Credentials.DBHost != "" {
		walgEnv = append(walgEnv, corev1.EnvVar{Name: "PGHOST", Value: job.Credentials.DBHost})
	}

	containers := make([]corev1.Container, len(existingContainers))
	copy(containers, existingContainers)
	for i, c := range containers {
		if c.Name == pgContainerName {
			// Switch from Alpine to Debian so glibc WAL-G binary works
			containers[i].Image = strings.Replace(c.Image, "-alpine", "", 1)

			// Remove previously injected dbpilot env vars and volume mounts
			cleanEnv := make([]corev1.EnvVar, 0)
			for _, e := range c.Env {
				if !strings.HasPrefix(e.Name, "WALG_") && e.Name != "AWS_ACCESS_KEY_ID" && e.Name != "AWS_SECRET_ACCESS_KEY" && e.Name != "AWS_DEFAULT_REGION" && e.Name != "AWS_ENDPOINT_URL" && e.Name != "PGUSER" && e.Name != "PGPASSWORD" && e.Name != "PGDATABASE" && e.Name != "PGHOST" {
					cleanEnv = append(cleanEnv, e)
				}
			}
			cleanMounts := make([]corev1.VolumeMount, 0)
			for _, vm := range c.VolumeMounts {
				if !strings.HasPrefix(vm.Name, "dbpilot") {
					cleanMounts = append(cleanMounts, vm)
				}
			}
			// Remove previously injected args
			cleanArgs := make([]string, 0)
			skipNext := false
			for _, a := range c.Args {
				if skipNext {
					skipNext = false
					continue
				}
				if a == "-c" {
					skipNext = true
					continue
				}
				cleanArgs = append(cleanArgs, a)
			}

			containers[i].Env = append(cleanEnv, append(walgEnv, corev1.EnvVar{Name: "SSL_CERT_DIR", Value: "/walg-bin/certs"})...)
			containers[i].VolumeMounts = append(cleanMounts, corev1.VolumeMount{Name: walgBinVolume, MountPath: "/walg-bin"})
			containers[i].Args = append(cleanArgs,
				"-c", "archive_mode=on",
				"-c", "archive_command=/walg-bin/wal-g wal-push %p",
				"-c", "archive_timeout=60",
			)
			break
		}
	}

	volumes := append(existingVols,
		corev1.Volume{
			Name:         walgBinVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	)

	return podSpecPatch{
		Spec: podSpecPatchSpec{
			Template: podSpecPatchTemplate{
				Spec: podSpecPatchPodSpec{
					InitContainers: append(existingInits, initContainer),
					Containers:     containers,
					Volumes:        volumes,
				},
			},
		},
	}
}
