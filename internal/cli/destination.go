package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
	"github.com/theomorin/dbpilot/internal/config"
)

type destChoice int

const (
	destS3    destChoice = 0
	destMinIO destChoice = 1
)

type destModel struct {
	cursor int
	done   bool
}

func (m destModel) Init() tea.Cmd { return nil }

func (m destModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < 1 {
				m.cursor++
			}
		case "enter":
			m.done = true
			return m, tea.Quit
		case "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m destModel) View() string {
	options := []string{
		"AWS S3         (native, no endpoint needed)",
		"S3-compatible  (MinIO, Scaleway, OVH, ...)",
	}
	var b strings.Builder
	b.WriteString("Select backup destination:\n")
	b.WriteString(styleSubtext.Render("  ↑/↓ navigate   enter confirm") + "\n\n")
	for i, opt := range options {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, opt))
	}
	return b.String()
}

// DestinationSetup holds the destination config and the raw S3 credentials.
type DestinationSetup struct {
	Destination config.DestinationConfig
	AccessKey   string
	SecretKey   string
}

// AskDestination interactively configures the backup destination and S3 credentials.
func AskDestination() (DestinationSetup, error) {
	result, err := tea.NewProgram(destModel{}).Run()
	if err != nil {
		return DestinationSetup{}, err
	}
	m := result.(destModel)

	dest := config.DestinationConfig{Type: "s3"}
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()

	fmt.Print("  Bucket name: ")
	bucket, _ := reader.ReadString('\n')
	dest.Bucket = strings.TrimSpace(bucket)

	if destChoice(m.cursor) == destMinIO {
		fmt.Print("  Endpoint URL (e.g. http://minio.example.com:9000): ")
		endpoint, _ := reader.ReadString('\n')
		dest.Endpoint = strings.TrimSpace(endpoint)
	} else {
		fmt.Print("  AWS region (e.g. eu-west-3): ")
		region, _ := reader.ReadString('\n')
		dest.Region = strings.TrimSpace(region)
	}

	fmt.Println()
	fmt.Print("  S3 Access Key: ")
	accessKey, _ := reader.ReadString('\n')

	fmt.Print("  S3 Secret Key: ")
	secretBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		// fallback if not a real TTY
		fallback, _ := reader.ReadString('\n')
		secretBytes = []byte(fallback)
	}

	return DestinationSetup{
		Destination: dest,
		AccessKey:   strings.TrimSpace(accessKey),
		SecretKey:   strings.TrimSpace(string(secretBytes)),
	}, nil
}
