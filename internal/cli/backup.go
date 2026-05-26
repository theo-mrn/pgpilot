package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:          "backup [job-name]",
	Short:        "Trigger a backup now for one or all jobs",
	SilenceUsage: true,
	RunE:         runBackup,
}

var flagBackupKubeconfig string
var flagBackupWait bool
var flagBackupDryRun bool

func init() {
	home, _ := os.UserHomeDir()
	backupCmd.Flags().StringVar(&flagBackupKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
	backupCmd.Flags().BoolVarP(&flagBackupWait, "wait", "w", false, "wait for backup to complete and show result")
	backupCmd.Flags().BoolVar(&flagBackupDryRun, "dry-run", false, "show what would be triggered without running")
}

func runBackup(cmd *cobra.Command, args []string) error {
	config, err := clientcmd.BuildConfigFromFlags("", flagBackupKubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	cronjobs, err := client.BatchV1().CronJobs("").List(context.Background(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=dbpilot",
	})
	if err != nil {
		return fmt.Errorf("listing cronjobs: %w", err)
	}
	if len(cronjobs.Items) == 0 {
		fmt.Println("No dbpilot CronJobs found. Run 'dbpilot deploy' first.")
		return nil
	}

	// Filter by name if argument provided
	var targets []batchv1.CronJob
	if len(args) > 0 {
		needle := args[0]
		for _, cj := range cronjobs.Items {
			if cj.Name == needle || cj.Labels["dbpilot/job"] == needle {
				targets = append(targets, cj)
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("no CronJob found matching %q", needle)
		}
	} else {
		targets = cronjobs.Items
	}

	if flagBackupDryRun {
		fmt.Println("Dry run — no jobs will be created.\n")
		for _, cj := range targets {
			ts := time.Now().Format("20060102-150405")
			fmt.Printf("  %s  %s  →  job/%s-manual-%s\n", styleCursor.Render("▶"), cj.Name, cj.Name, ts)
		}
		return nil
	}

	var triggered []triggeredJob
	for _, cj := range targets {
		job, err := triggerNow(client, cj)
		if err != nil {
			fmt.Printf("  %s  %s: %v\n", styleErr.Render("✗"), cj.Name, err)
			continue
		}
		triggered = append(triggered, triggeredJob{client: client, job: job, cronName: cj.Name})
		fmt.Printf("  %s  %s  →  job/%s\n", styleOK.Render("▶"), cj.Name, job.Name)
	}

	if !flagBackupWait || len(triggered) == 0 {
		return nil
	}

	fmt.Println()
	for _, t := range triggered {
		fmt.Printf("── %s ──\n", t.cronName)
		status, err := waitForJobWithLogs(t.client, t.job, 10*time.Minute)
		if err != nil {
			fmt.Printf("%s %s: %v\n\n", styleErr.Render("✗"), t.cronName, err)
			continue
		}
		if status == "success" {
			fmt.Printf("%s %s — done\n\n", styleOK.Render("✓"), t.cronName)
		} else {
			fmt.Printf("%s %s: %s\n\n", styleErr.Render("✗"), t.cronName, status)
		}
	}
	return nil
}

type triggeredJob struct {
	client   kubernetes.Interface
	job      *batchv1.Job
	cronName string
}

func triggerNow(client kubernetes.Interface, cj batchv1.CronJob) (*batchv1.Job, error) {
	ts := time.Now().Format("20060102-150405")
	jobName := fmt.Sprintf("%s-manual-%s", cj.Name, ts)
	// Truncate from the middle to keep timestamp suffix intact
	if len(jobName) > 63 {
		keep := 63 - len(ts) - 8 // "-manual-" = 8 chars
		jobName = cj.Name[:keep] + "-manual-" + ts
	}

	t := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cj.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/job":                  cj.Labels["dbpilot/job"],
				"dbpilot/trigger":              "manual",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "batch/v1",
					Kind:               "CronJob",
					Name:               cj.Name,
					UID:                cj.UID,
					Controller:         &t,
					BlockOwnerDeletion: &t,
				},
			},
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}

	return client.BatchV1().Jobs(cj.Namespace).Create(context.Background(), job, metav1.CreateOptions{})
}

func waitForJobWithLogs(client kubernetes.Interface, job *batchv1.Job, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	reportedPending := make(map[string]bool)

	for time.Now().Before(deadline) {
		j, err := client.BatchV1().Jobs(job.Namespace).Get(context.Background(), job.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}

		if j.Status.Succeeded > 0 {
			// Print logs from the completed pod
			pods, _ := client.CoreV1().Pods(job.Namespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name),
			})
			for _, pod := range pods.Items {
				logs := getPodLogs(client, pod.Namespace, pod.Name)
				if logs != "" {
					fmt.Printf("  pod/%s:\n\n%s\n", pod.Name, logs)
				}
			}
			return "success", nil
		}
		if j.Status.Failed > 0 {
			pods, _ := client.CoreV1().Pods(job.Namespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name),
			})
			for _, pod := range pods.Items {
				logs := getPodLogs(client, pod.Namespace, pod.Name)
				if logs != "" {
					fmt.Printf("  pod/%s:\n\n%s\n", pod.Name, logs)
				}
			}
			return fmt.Sprintf("failed (%d attempt(s))", j.Status.Failed), nil
		}

		// Show pending reason while waiting
		pods, _ := client.CoreV1().Pods(job.Namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name),
		})
		for _, pod := range pods.Items {
			if pod.Status.Phase == "Pending" && !reportedPending[pod.Name] {
				reason := pendingReason(pod)
				if reason != "waiting for scheduler" {
					fmt.Printf("  pod/%s — %s\n", pod.Name, reason)
					reportedPending[pod.Name] = true
				}
			}
		}

		time.Sleep(3 * time.Second)
	}
	return "timeout", nil
}

// pendingReason returns a human-readable reason why a pod is still pending.
func pendingReason(pod corev1.Pod) string {
	// Check container statuses first
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			return fmt.Sprintf("%s: %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
	}
	// Check pod conditions
	for _, c := range pod.Status.Conditions {
		if c.Status == "False" && c.Message != "" {
			return c.Message
		}
	}
	// Fallback to pod phase reason
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return "waiting for scheduler"
}

func getPodLogs(client kubernetes.Interface, namespace, podName string) string {
	req := client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	data, err := req.DoRaw(context.Background())
	if err != nil {
		return ""
	}
	return string(data)
}
