package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
)

var backupCmd = &cobra.Command{
	Use:          "backup <config> [job-name]",
	Short:        "Trigger a backup now for one or all jobs in a config",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	RunE:         runBackup,
}

var flagBackupKubeconfig string
var flagBackupWait bool
var flagBackupDryRun bool

func init() {
	home, _ := os.UserHomeDir()
	backupCmd.Flags().StringVar(&flagBackupKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
	backupCmd.Flags().BoolVarP(&flagBackupWait, "no-wait", "n", false, "return immediately without waiting for completion")
	backupCmd.Flags().BoolVar(&flagBackupDryRun, "dry-run", false, "show what would be triggered without running")
}

func runBackup(cmd *cobra.Command, args []string) error {
	cfgPath, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if len(cfg.Jobs) == 0 {
		return fmt.Errorf("config %q has no jobs", args[0])
	}

	k8scfg, err := clientcmd.BuildConfigFromFlags("", flagBackupKubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return err
	}

	// Determine which jobs to trigger
	var jobNames []string
	if len(args) == 2 {
		jobNames = []string{args[1]}
	} else {
		for _, j := range cfg.Jobs {
			jobNames = append(jobNames, j.Name)
		}
	}

	// Find matching CronJobs in K8s
	var targets []batchv1.CronJob
	for _, jobName := range jobNames {
		cronName := "dbpilot-" + jobName
		// Find the job config to get its namespace
		var ns string
		for _, j := range cfg.Jobs {
			if j.Name == jobName {
				ns = j.Environment.Namespace
				break
			}
		}
		if ns == "" {
			fmt.Printf("  %s  %s: not found in config\n", styleErr.Render("✗"), jobName)
			continue
		}
		cj, err := client.BatchV1().CronJobs(ns).Get(context.Background(), cronName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("  %s  %s: CronJob not found — run 'dbpilot deploy %s' first\n", styleErr.Render("✗"), jobName, args[0])
			continue
		}
		targets = append(targets, *cj)
	}

	if len(targets) == 0 {
		return fmt.Errorf("no jobs to trigger")
	}

	if flagBackupDryRun {
		fmt.Println("Dry run — no jobs will be created.\n")
		for _, cj := range targets {
			ts := time.Now().Format("20060102-150405")
			fmt.Printf("  %s  %s  →  job/%s-manual-%s\n", styleCursor.Render("▶"), cj.Name, cj.Name, ts)
		}
		return nil
	}

	// Collect unique bucket names
	buckets := make(map[string]bool)
	for _, j := range cfg.Jobs {
		for _, d := range j.Destinations {
			if d.Bucket != "" {
				buckets[d.Bucket] = true
			}
		}
	}
	bucketList := make([]string, 0, len(buckets))
	for b := range buckets {
		bucketList = append(bucketList, b)
	}
	fmt.Printf("Backing up %d database(s) from config %q → s3://%s\n\n", len(targets), args[0], strings.Join(bucketList, ", s3://"))

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

	if flagBackupWait || len(triggered) == 0 {
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
	if len(jobName) > 63 {
		keep := 63 - len(ts) - 8
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

func pendingReason(pod corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			return fmt.Sprintf("%s: %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Status == "False" && c.Message != "" {
			return c.Message
		}
	}
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
