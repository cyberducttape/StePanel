package stepanel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	setupguide "github.com/cyberducttape/StePanel/internal/setup"
	"golang.org/x/term"
)

// existingInstallEnv is the configuration an installed StePanel runs with.
const existingInstallEnv = "/etc/ste-panel.env"

// runSetupCommand implements `stepanel setup`: the guided first-time setup
// that writes the installer settings file. `stepanel init` is an alias.
func runSetupCommand(args []string) {
	if err := runSetup(args, os.Stdin, os.Stdout, newSetupHost(), existingInstallEnv); err != nil {
		if errors.Is(err, setupguide.ErrCancelled) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(1)
	}
}

func runSetup(args []string, in *os.File, out io.Writer, host setupguide.Host, existingEnv string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(out)
	output := flags.String("output", "stepanel-install.env", "where to write the installer settings")
	force := flags.Bool("force", false, "replace an existing settings file")
	fromInstaller := flags.Bool("from-installer", false, "invoked by install.sh --guided")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(existingEnv); err == nil {
		// New keys would orphan existing encrypted backups and break the
		// audit chain, so the guide never runs against an installed host.
		return fmt.Errorf("StePanel is already installed here (%s). To upgrade, run `sudo ./install.sh`; your settings and keys are kept", existingEnv)
	}
	if _, err := os.Lstat(*output); err == nil && !*force {
		return fmt.Errorf("%s already exists; use it with `sudo ./install.sh --config %s`, or pass --force to replace it (its keys will be lost)", *output, *output)
	}
	answers, err := setupguide.NewGuide(newSetupTerminal(in), out, host).Run()
	if err != nil {
		return err
	}
	data, err := setupguide.EncodeEnvFile(answers.Env())
	if err != nil {
		return err
	}
	path, err := filepath.Abs(*output)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", *output, err)
	}
	if err := writeAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	fmt.Fprintf(out, "\n✓ Settings saved to %s (readable only by its owner).\n", path)
	fmt.Fprintln(out, "  Keep a copy somewhere safe and offline: it holds the backup encryption")
	fmt.Fprintln(out, "  key, and without it encrypted backups cannot be restored.")
	if !*fromInstaller {
		fmt.Fprintln(out, "\nNext:")
		fmt.Fprintf(out, "  1. Preview the changes:  sudo ./install.sh --config %s --dry-run\n", path)
		fmt.Fprintf(out, "  2. Install:              sudo ./install.sh --config %s\n", path)
	}
	return nil
}

// setupTerminal reads lines from stdin and secrets without echo when stdin
// is a terminal. Piped input is read line by line for automation and tests.
type setupTerminal struct {
	file   *os.File
	reader *bufio.Reader
}

func newSetupTerminal(file *os.File) *setupTerminal {
	return &setupTerminal{file: file, reader: bufio.NewReader(file)}
}

func (t *setupTerminal) ReadLine() (string, error) {
	line, err := t.reader.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (t *setupTerminal) ReadSecret() (string, error) {
	if term.IsTerminal(int(t.file.Fd())) {
		secret, err := term.ReadPassword(int(t.file.Fd()))
		return string(secret), err
	}
	return t.ReadLine()
}

// setupHost connects the guide to this host.
type setupHost struct{}

func newSetupHost() setupHost { return setupHost{} }

func (setupHost) HashPassword(password string) (string, error) { return hashPassword(password) }

func (setupHost) VerifyTOTP(secret, code string, now time.Time) bool {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false
	}
	counter := now.Unix() / 30
	for drift := int64(-1); drift <= 1; drift++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, uint64(counter+drift))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func (setupHost) Now() time.Time { return time.Now().UTC() }

func (setupHost) Hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func (setupHost) Rclone() bool {
	_, err := exec.LookPath("rclone")
	return err == nil
}

func rcloneOutput(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "rclone", args...).Output()
	if err != nil {
		return "", fmt.Errorf("rclone %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func (setupHost) RcloneRemotes() ([]string, error) {
	output, err := rcloneOutput("listremotes")
	if err != nil {
		return nil, err
	}
	return strings.Fields(output), nil
}

func (setupHost) RcloneConfigFile() (string, error) {
	output, err := rcloneOutput("config", "file")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(output)
	if len(lines) == 0 {
		return "", errors.New("rclone did not report its configuration file")
	}
	path := lines[len(lines)-1]
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("rclone configuration path %q is not absolute", path)
	}
	return path, nil
}

func (setupHost) ProbeOffsite(target string) error {
	if err := validateOffsiteTarget(target); err != nil {
		return err
	}
	return probeOffsiteRemote(target)
}

func (setupHost) SSHClientIP() string {
	fields := strings.Fields(os.Getenv("SSH_CLIENT"))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (setupHost) Random(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
