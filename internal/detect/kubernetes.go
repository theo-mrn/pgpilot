package detect

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// engineProfile defines all signals used to score a database engine.
// serverEnvVars are set only on the server itself (e.g. POSTGRES_DB initializes the DB).
// clientEnvVars may appear on client containers too — only counted when the standard
// port is also exposed, to avoid false positives on app containers.
type engineProfile struct {
	engine        DatabaseEngine
	defaultPort   int32
	imageKeywords []string
	serverEnvVars []string // +3 unconditionally
	clientEnvVars []string // +3 only when port is also exposed
	nameKeywords  []string // +1, engine-specific only (no generic "db")
	labelValues   []string // +1
}

var engineProfiles = []engineProfile{
	{
		engine:        EnginePostgres,
		defaultPort:   5432,
		imageKeywords: []string{"postgres", "postgresql", "pgvector", "timescale", "postgis", "bitnami/postgresql"},
		serverEnvVars: []string{"POSTGRES_DB", "POSTGRES_USER", "PGDATA"},
		clientEnvVars: []string{"POSTGRES_PASSWORD"},
		nameKeywords:  []string{"postgres", "postgresql", "pgsql"},
		labelValues:   []string{"postgres", "postgresql"},
	},
	{
		engine:        EngineMySQL,
		defaultPort:   3306,
		imageKeywords: []string{"mysql", "mariadb", "percona-server", "bitnami/mysql", "bitnami/mariadb"},
		serverEnvVars: []string{"MYSQL_DATABASE", "MYSQL_USER", "MARIADB_DATABASE"},
		clientEnvVars: []string{"MYSQL_ROOT_PASSWORD"},
		nameKeywords:  []string{"mysql", "mariadb"},
		labelValues:   []string{"mysql", "mariadb"},
	},
	{
		engine:        EngineMongoDB,
		defaultPort:   27017,
		imageKeywords: []string{"mongo", "mongodb", "bitnami/mongodb"},
		serverEnvVars: []string{"MONGO_INITDB_DATABASE", "MONGO_INITDB_ROOT_USERNAME"},
		clientEnvVars: []string{},
		nameKeywords:  []string{"mongo", "mongodb"},
		labelValues:   []string{"mongo", "mongodb"},
	},
	{
		engine:        EngineRedis,
		defaultPort:   6379,
		imageKeywords: []string{"redis", "valkey", "keydb", "bitnami/redis", "bitnami/valkey"},
		serverEnvVars: []string{"REDIS_AOF_ENABLED", "VALKEY_PASSWORD"},
		clientEnvVars: []string{"REDIS_PASSWORD"},
		nameKeywords:  []string{"redis", "valkey", "keydb"},
		labelValues:   []string{"redis", "valkey", "keydb"},
	},
}

// ScanKubernetes scans all namespaces in the current kubeconfig context and
// returns every running pod that looks like a database instance.
// Low-confidence instances are excluded unless verbose is true.
func ScanKubernetes(kubeconfig string, verbose bool) ([]DetectedInstance, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	pods, err := client.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	var instances []DetectedInstance
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			engine, score, signals := scoreContainer(container, pod.Name, pod.Labels)
			confidence := confidenceFromScore(score)
			if confidence == ConfidenceNone {
				continue
			}
			if confidence == ConfidenceLow && !verbose {
				continue
			}
			instances = append(instances, DetectedInstance{
				Environment:      EnvKubernetes,
				Engine:           engine,
				EngineVersion:    extractVersion(container.Image),
				Namespace:        pod.Namespace,
				PodName:          pod.Name,
				Container:        container.Name,
				DisplayName:      fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
				DetectionSignals: signals,
				Score:            score,
				Confidence:       confidence,
			})
		}
	}
	return instances, nil
}

// scoreContainer runs the multi-signal confidence scorer (ADR-011) against a container.
// Returns the best-matching engine, its total score, and the contributing signals.
func scoreContainer(c corev1.Container, podName string, labels map[string]string) (DatabaseEngine, int, []string) {
	image := strings.ToLower(c.Image)
	name := strings.ToLower(c.Name + " " + podName)

	envByName := make(map[string]bool)
	for _, e := range c.Env {
		envByName[e.Name] = true
	}

	exposedPorts := make(map[int32]bool)
	for _, p := range c.Ports {
		exposedPorts[p.ContainerPort] = true
	}

	var bestEngine DatabaseEngine = EngineUnknown
	bestScore := 0
	var bestSignals []string

	for _, p := range engineProfiles {
		score := 0
		var signals []string

		// Signal 1 — image name (+3)
		for _, kw := range p.imageKeywords {
			if strings.Contains(image, kw) {
				score += 3
				signals = append(signals, fmt.Sprintf("image:%s", kw))
				break
			}
		}

		// Signal 2a — server-only env vars (+3 unconditionally)
		for _, envName := range p.serverEnvVars {
			if envByName[envName] {
				score += 3
				signals = append(signals, fmt.Sprintf("env:%s", envName))
				break
			}
		}

		// Signal 2b — client env vars (+3 only when port is also exposed)
		if exposedPorts[p.defaultPort] {
			for _, envName := range p.clientEnvVars {
				if envByName[envName] {
					score += 3
					signals = append(signals, fmt.Sprintf("env:%s", envName))
					break
				}
			}
		}

		// Signal 3 — standard port exposed (+2)
		if exposedPorts[p.defaultPort] {
			score += 2
			signals = append(signals, fmt.Sprintf("port:%d", p.defaultPort))
		}

		// Signal 4 — K8s labels (+1)
		// Only consider meaningful labels, not generated ones (revision-hash, chart version, etc.)
		meaningfulLabelKeys := []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/component", "app.kubernetes.io/instance"}
		for _, lv := range p.labelValues {
			for _, mk := range meaningfulLabelKeys {
				if v, ok := labels[mk]; ok && strings.Contains(strings.ToLower(v), lv) {
					score += 1
					signals = append(signals, fmt.Sprintf("label:%s=%s", mk, v))
					goto nextLabel
				}
			}
		nextLabel:
		}

		// Signal 5 — pod/container name, engine-specific only (+1)
		for _, kw := range p.nameKeywords {
			if strings.Contains(name, kw) {
				score += 1
				signals = append(signals, fmt.Sprintf("name:%s", kw))
				break
			}
		}

		if score > bestScore {
			bestScore = score
			bestEngine = p.engine
			bestSignals = signals
		}
	}

	return bestEngine, bestScore, bestSignals
}

// extractVersion parses a major version number from an image tag.
// "postgres:16-alpine" → "16", "bitnami/postgresql:15.3.0" → "15", "" if unknown.
func extractVersion(image string) string {
	colonIdx := indexOf(image, ':')
	if colonIdx < 0 {
		return ""
	}
	tag := image[colonIdx+1:]
	end := 0
	for end < len(tag) && tag[end] >= '0' && tag[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return tag[:end]
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
