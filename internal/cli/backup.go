package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/storage"
)

// backupCmd is the top-level "backup" group.
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Manage and trigger backups",
}

func newBackupNameCmd(cfgName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   cfgName,
		Short: fmt.Sprintf("Backup commands for config %q", cfgName),
	}

	runCmd := &cobra.Command{
		Use:          "run [job-name]",
		Short:        "Trigger a backup now",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			noWait, _ := c.Flags().GetBool("no-wait")
			dryRun, _ := c.Flags().GetBool("dry-run")
			kubeconfig, _ := c.Flags().GetString("kubeconfig")
			jobName := ""
			if len(args) > 0 {
				jobName = args[0]
			}
			return runBackupRun(cfgName, jobName, kubeconfig, noWait, dryRun)
		},
	}
	home, _ := os.UserHomeDir()
	runCmd.Flags().String("kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
	runCmd.Flags().Bool("no-wait", false, "return immediately without waiting for completion")
	runCmd.Flags().Bool("dry-run", false, "show what would be triggered without running")

	listCmd := &cobra.Command{
		Use:          "list [job-name]",
		Short:        "List available backups in S3",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			jobName := ""
			if len(args) > 0 {
				jobName = args[0]
			}
			kubeconfig, _ := c.Flags().GetString("kubeconfig")
			return runBackupList(cfgName, jobName, kubeconfig)
		},
	}
	listCmd.Flags().String("kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")

	cmd.AddCommand(runCmd)
	cmd.AddCommand(listCmd)
	return cmd
}

func runBackupRun(cfgName, jobName, kubeconfig string, noWait, dryRun bool) error {
	cfgPath, err := config.NamedPath(cfgName)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		return err
	}

	var jobNames []string
	if jobName != "" {
		jobNames = []string{jobName}
	} else {
		for _, j := range cfg.Jobs {
			jobNames = append(jobNames, j.Name)
		}
	}

	var targets []batchv1.CronJob
	for _, name := range jobNames {
		cronName := "dbpilot-" + name
		var ns string
		for _, j := range cfg.Jobs {
			if j.Name == name {
				ns = j.Environment.Namespace
				break
			}
		}
		if ns == "" {
			fmt.Printf("  %s  %s: not found in config\n", styleErr.Render("✗"), name)
			continue
		}
		cj, err := client.BatchV1().CronJobs(ns).Get(context.Background(), cronName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("  %s  %s: CronJob not found — run 'dbpilot deploy %s' first\n", styleErr.Render("✗"), name, cfgName)
			continue
		}
		targets = append(targets, *cj)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no jobs to trigger")
	}

	if dryRun {
		fmt.Println("Dry run — no jobs will be created.\n")
		for _, cj := range targets {
			ts := time.Now().Format("20060102-150405")
			fmt.Printf("  %s  %s  →  job/%s-manual-%s\n", styleCursor.Render("▶"), cj.Name, cj.Name, ts)
		}
		return nil
	}

	// Collect unique buckets for display
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
	fmt.Printf("Backing up %d database(s) from config %q → s3://%s\n\n", len(targets), cfgName, strings.Join(bucketList, ", s3://"))

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

	if noWait || len(triggered) == 0 {
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

func runBackupList(cfgName, jobName, kubeconfig string) error {
	cfgPath, err := config.NamedPath(cfgName)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	for _, job := range cfg.Jobs {
		if jobName != "" && job.Name != jobName {
			continue
		}
		if len(job.Destinations) == 0 {
			continue
		}
		dest := job.Destinations[0]
		accessKey, err := k8sReadSecret(kubeconfig, dest.S3AccessKey.From)
		if err != nil {
			fmt.Printf("  %s  %s: %v\n", styleErr.Render("✗"), job.Name, err)
			continue
		}
		secretKey, err := k8sReadSecret(kubeconfig, dest.S3SecretKey.From)
		if err != nil {
			fmt.Printf("  %s  %s: %v\n", styleErr.Render("✗"), job.Name, err)
			continue
		}
		objects, err := storage.ListBackups(dest.Bucket, dest.Prefix, accessKey, secretKey, dest.Region, dest.Endpoint)
		if err != nil {
			fmt.Printf("  %s  %s: %v\n", styleErr.Render("✗"), job.Name, err)
			continue
		}
		fmt.Printf("\n%s — s3://%s/%s (%d backup(s))\n", job.Name, dest.Bucket, dest.Prefix, len(objects))
		for i := len(objects) - 1; i >= 0; i-- {
			o := objects[i]
			parts := strings.Split(o.Key, "/")
			fmt.Printf("  %s  %s  %s\n", styleSubtext.Render(o.LastModified), parts[len(parts)-1], formatSize(o.Size))
		}
	}
	return nil
}

func k8sReadSecret(kubeconfig, ref string) (string, error) {
	return k8sReadSecretInternal(kubeconfig, ref)
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
				if logs := getPodLogs(client, pod.Namespace, pod.Name); logs != "" {
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
				if logs := getPodLogs(client, pod.Namespace, pod.Name); logs != "" {
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
				if reason := pendingReason(pod); reason != "waiting for scheduler" {
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

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func init() {
	// Register known configs as sub-groups at startup
	if dir, err := config.ConfigDir(); err == nil {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					n := strings.TrimSuffix(e.Name(), ".yaml")
					backupCmd.AddCommand(newBackupNameCmd(n))
				}
			}
		}
	}
}
