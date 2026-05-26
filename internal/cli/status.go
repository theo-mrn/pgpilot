package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:          "status [name]",
	Short:        "Show status of deployed backup CronJobs",
	Long:         "Show status of all deployed CronJobs, or only those from a specific config if <name> is given.",
	SilenceUsage: true,
	RunE:         runStatus,
}

var flagStatusKubeconfig string

func init() {
	home, _ := os.UserHomeDir()
	statusCmd.Flags().StringVar(&flagStatusKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
}

func runStatus(cmd *cobra.Command, args []string) error {
	config, err := clientcmd.BuildConfigFromFlags("", flagStatusKubeconfig)
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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "\tJOB\tNAMESPACE\tSCHEDULE\tLAST RUN\tSTATUS")
	fmt.Fprintln(w, "\t---\t---------\t--------\t--------\t------")

	for _, cj := range cronjobs.Items {
		marker, lastRun, status := cronJobStatus(client, cj)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			marker,
			cj.Name,
			cj.Namespace,
			cj.Spec.Schedule,
			lastRun,
			status,
		)
	}
	w.Flush()

	fmt.Printf("\n%d CronJob(s)\n", len(cronjobs.Items))
	return nil
}

func cronJobStatus(client kubernetes.Interface, cj batchv1.CronJob) (marker, lastRun, status string) {
	if cj.Status.LastScheduleTime == nil {
		return styleSubtext.Render("~"), "never", "waiting for first run"
	}

	lastRun = humanDuration(time.Since(cj.Status.LastScheduleTime.Time))

	// Find the most recent completed job
	jobs, err := client.BatchV1().Jobs(cj.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("batch.kubernetes.io/controller-uid=%s", string(cj.UID)),
	})
	if err != nil || len(jobs.Items) == 0 {
		// Fallback: check active jobs
		if len(cj.Status.Active) > 0 {
			return styleCursor.Render("▶"), lastRun, "running"
		}
		return styleSubtext.Render("~"), lastRun, "unknown"
	}

	// Find most recent job
	var latest *batchv1.Job
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if latest == nil || j.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = j
		}
	}

	if latest.Status.Succeeded > 0 {
		return styleOK.Render("✓"), lastRun, "success"
	}
	if latest.Status.Failed > 0 {
		return styleErr.Render("✗"), lastRun, fmt.Sprintf("failed (%d attempt(s))", latest.Status.Failed)
	}
	if len(cj.Status.Active) > 0 {
		return styleCursor.Render("▶"), lastRun, "running"
	}
	return styleSubtext.Render("~"), lastRun, "unknown"
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}
}

func humanTime(t *metav1.Time) string {
	if t == nil {
		return "—"
	}
	return strings.Replace(t.Format("2006-01-02 15:04"), " ", " ", 1)
}
