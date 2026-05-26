package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/detect"
	"github.com/theomorin/dbpilot/internal/k8s"
)

// ResolvedJobCredentials holds all resolved secret refs for one job.
type ResolvedJobCredentials struct {
	Password string
	User     string
	Name     string
}

// ResolveDBCredentials scans each selected instance's namespace for existing
// Secrets and returns resolved credential refs per job name.
func ResolveDBCredentials(kubeconfig string, instances []detect.DetectedInstance) (map[string]ResolvedJobCredentials, error) {
	reader := bufio.NewReader(os.Stdin)
	result := make(map[string]ResolvedJobCredentials)

	fmt.Println("\nResolving database credentials...\n")

	for _, inst := range instances {
		jobName := inst.Namespace + "-" + jobNameFromPod(inst.PodName)
		fmt.Printf("  %s (%s/%s)\n", jobName, inst.Namespace, inst.PodName)

		creds, err := k8s.ScanDBCredentials(kubeconfig, inst.Namespace, inst.PodName)
		if err != nil {
			fmt.Printf("    %s  scan failed: %v\n", styleErr.Render("✗"), err)
		}

		resolved := ResolvedJobCredentials{}

		resolved.Password = resolveRef(reader, "password", creds.Password)
		resolved.User = resolveRef(reader, "user    ", creds.User)
		resolved.Name = resolveRef(reader, "db_name ", creds.Name)

		result[jobName] = resolved
		fmt.Println()
	}

	return result, nil
}

// resolveRef displays a found ref and asks for confirmation, or prompts for manual input.
func resolveRef(reader *bufio.Reader, label string, ref *k8s.DBCredentialRef) string {
	if ref != nil {
		s := ref.SecretRefString()
		fmt.Printf("    %s  %s: %s\n", styleOK.Render("✓"), label, s)
		fmt.Print("    Use this? [Y/n]: ")
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "n" {
			return s
		}
	} else {
		fmt.Printf("    %s  %s: not found\n", styleUnsupported.Render("~"), label)
	}

	fmt.Printf("    Enter secret ref for %s (or leave empty to skip): ", strings.TrimSpace(label))
	manual, _ := reader.ReadString('\n')
	return strings.TrimSpace(manual)
}

// ApplyDBCredentials updates each job's credential refs from the resolved map.
func ApplyDBCredentials(cfg *config.BackupConfig, credRefs map[string]ResolvedJobCredentials) {
	for i, job := range cfg.Jobs {
		if resolved, ok := credRefs[job.Name]; ok {
			if resolved.Password != "" {
				cfg.Jobs[i].Credentials.DBPassword = config.SecretRef{From: resolved.Password}
			}
			if resolved.User != "" {
				cfg.Jobs[i].Credentials.DBUser = config.SecretRef{From: resolved.User}
			}
			if resolved.Name != "" {
				cfg.Jobs[i].Credentials.DBName = config.SecretRef{From: resolved.Name}
			}
		}
	}
}

// jobNameFromPod strips the random suffix from a pod name.
func jobNameFromPod(podName string) string {
	parts := strings.Split(podName, "-")
	if len(parts) > 2 {
		last := parts[len(parts)-1]
		isOrdinal := true
		for _, c := range last {
			if c < '0' || c > '9' {
				isOrdinal = false
				break
			}
		}
		if !isOrdinal {
			parts = parts[:len(parts)-2]
		}
	}
	return strings.Join(parts, "-")
}
