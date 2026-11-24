package target

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// GrepPatterns is the only text runrack ever searches for inside runner logs.
var GrepPatterns = []string{
	"StepsRunner] Processing step: DisplayName=",
	"Job result after all job steps finish: ",
	"with current step result '",
	"Listener] Version: ",
	"Adding option 'labels': ",
	"Runner is built for Linux (",
}

var deniedBasenames = []string{".credentials", ".credentials_rsaparams", ".env", ".path"}

var (
	containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
	runnerUnit    = regexp.MustCompile(`^actions\.runner\.[A-Za-z0-9._@-]+\.service$`)
	unitListArgv  = []string{"systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "actions.runner.*"}
)

// Check is the safety boundary: every command runrack runs must pass it.
func Check(argv []string) error {
	if slices.Equal(argv, []string{"incus", "list", "-f", "json"}) || slices.Equal(argv, []string{"gh", "auth", "token"}) ||
		slices.Equal(argv, tmuxClientTermArgv) {
		return nil
	}
	inner := argv
	if len(argv) >= 4 && argv[0] == "incus" && argv[1] == "exec" && argv[3] == "--" {
		if !containerName.MatchString(argv[2]) {
			return fmt.Errorf("allowlist: bad container name %q", argv[2])
		}
		inner = argv[4:]
	}
	if err := checkInner(inner, len(inner) != len(argv)); err != nil {
		return fmt.Errorf("allowlist: %q: %w", strings.Join(argv, " "), err)
	}
	return nil
}

func checkInner(argv []string, inContainer bool) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	switch argv[0] {
	case "systemctl":
		if slices.Equal(argv, unitListArgv) {
			return nil
		}
		if len(argv) == 5 && argv[1] == "show" && argv[2] == "-p" && argv[3] == "WorkingDirectory" && runnerUnit.MatchString(argv[4]) {
			return nil
		}
		return fmt.Errorf("systemctl call not allowed")
	}
	if !inContainer {
		return fmt.Errorf("file reads on the host never run commands")
	}
	switch argv[0] {
	case "head":
		if len(argv) != 4 || argv[1] != "-c" {
			return fmt.Errorf("head shape not allowed")
		}
		if n, err := strconv.ParseInt(argv[2], 10, 64); err != nil || n <= 0 {
			return fmt.Errorf("head byte count %q", argv[2])
		}
		return CheckPath(argv[3])
	case "ls":
		if len(argv) != 3 || argv[1] != "-1" {
			return fmt.Errorf("ls shape not allowed")
		}
		return CheckPath(argv[2])
	case "grep":
		return checkGrep(argv)
	}
	return fmt.Errorf("command %q not allowed", argv[0])
}

func checkGrep(argv []string) error {
	if len(argv) < 7 || !slices.Equal(argv[1:4], []string{"-a", "-h", "-F"}) {
		return fmt.Errorf("grep shape not allowed")
	}
	args := argv[4 : len(argv)-1]
	if len(args)%2 != 0 {
		return fmt.Errorf("grep patterns must be -e pairs")
	}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-e" || !slices.Contains(GrepPatterns, args[i+1]) {
			return fmt.Errorf("grep pattern %q not allowed", args[i+1])
		}
	}
	return CheckPath(argv[len(argv)-1])
}

func CheckPath(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "..") {
		return fmt.Errorf("path %q must be absolute and clean", p)
	}
	if slices.Contains(deniedBasenames, path.Base(p)) {
		return fmt.Errorf("path %q is never read", p)
	}
	return nil
}
