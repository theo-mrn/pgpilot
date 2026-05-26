package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	agepkg "github.com/theomorin/dbpilot/internal/age"
)

var styleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
var styleKey = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

// KeyResult holds both keys after generation and confirmation.
type KeyResult struct {
	PublicKey  string
	PrivateKey string
}

// GenerateAndConfirmKey generates an age key pair, displays the private key,
// and blocks until the user confirms they have saved it.
func GenerateAndConfirmKey() (KeyResult, error) {
	kp, err := agepkg.GenerateKeyPair()
	if err != nil {
		return KeyResult{}, err
	}

	fmt.Println()
	fmt.Println(styleWarning.Render("⚠  Encryption key generated — save your private key NOW"))
	fmt.Println()
	fmt.Println("  Private key (will also be stored in K8s Secret dbpilot/dbpilot-age-key):")
	fmt.Println()
	fmt.Println("  " + styleKey.Render(kp.PrivateKey))
	fmt.Println()
	fmt.Println("  Public key (written to backup.yaml — safe to commit):")
	fmt.Println("  " + kp.PublicKey)
	fmt.Println()
	fmt.Println(styleWarning.Render("  Keep a copy of the private key outside the cluster."))
	fmt.Println(styleWarning.Render("  If both the cluster and this key are lost, backups are unrecoverable."))
	fmt.Println()

	if err := confirmKeySaved(); err != nil {
		return KeyResult{}, err
	}

	return KeyResult{PublicKey: kp.PublicKey, PrivateKey: kp.PrivateKey}, nil
}

func confirmKeySaved() error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("  Type YES to confirm you have saved the private key: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if strings.TrimSpace(input) == "YES" {
			return nil
		}
		fmt.Println(styleWarning.Render("  Please type YES (uppercase) to continue."))
	}
}
