package target

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	execTimeout   = 3 * time.Second
	perTargetJobs = 2
)

type Target interface {
	ID() string
	Head(ctx context.Context, path string, maxBytes int64) ([]byte, error)
	List(ctx context.Context, dir string) ([]string, error)
	Grep(ctx context.Context, path string, fixed ...string) ([]string, error)
	Units(ctx context.Context) ([]Unit, error)
	WorkingDirectory(ctx context.Context, unit string) (string, error)
}

type Unit struct {
	Name   string
	Active string
	Sub    string
}

const HostID = "host"

func IncusID(container string) string { return "incus:" + container }

type HostTarget struct{ slots chan struct{} }

func NewHost() *HostTarget { return &HostTarget{slots: make(chan struct{}, perTargetJobs)} }

func (h *HostTarget) ID() string { return HostID }

func (h *HostTarget) Head(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxBytes))
}

func (h *HostTarget) List(_ context.Context, dir string) ([]string, error) {
	if err := CheckPath(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names, nil
}

func (h *HostTarget) Grep(_ context.Context, path string, fixed ...string) ([]string, error) {
	if err := checkPatterns(fixed); err != nil {
		return nil, err
	}
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if slices.ContainsFunc(fixed, func(s string) bool { return strings.Contains(line, s) }) {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

func (h *HostTarget) Units(ctx context.Context) ([]Unit, error) {
	out, err := run(ctx, h.slots, unitListArgv)
	if err != nil {
		return nil, err
	}
	return ParseUnits(string(out)), nil
}

func (h *HostTarget) WorkingDirectory(ctx context.Context, unit string) (string, error) {
	out, err := run(ctx, h.slots, workingDirectoryArgv(unit))
	if err != nil {
		return "", err
	}
	return parseWorkingDirectory(out)
}

type IncusTarget struct {
	Container string
	slots     chan struct{}
}

func NewIncus(container string) *IncusTarget {
	return &IncusTarget{Container: container, slots: make(chan struct{}, perTargetJobs)}
}

func (t *IncusTarget) ID() string { return IncusID(t.Container) }

func (t *IncusTarget) exec(ctx context.Context, argv ...string) ([]byte, error) {
	return run(ctx, t.slots, append([]string{"incus", "exec", t.Container, "--"}, argv...))
}

func (t *IncusTarget) Head(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	return t.exec(ctx, "head", "-c", strconv.FormatInt(maxBytes, 10), path)
}

func (t *IncusTarget) List(ctx context.Context, dir string) ([]string, error) {
	out, err := t.exec(ctx, "ls", "-1", dir)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(string(out)), nil
}

func (t *IncusTarget) Grep(ctx context.Context, path string, fixed ...string) ([]string, error) {
	argv := []string{"grep", "-a", "-h", "-F"}
	for _, pattern := range fixed {
		argv = append(argv, "-e", pattern)
	}
	out, err := t.exec(ctx, append(argv, path)...)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(string(out)), nil
}

func (t *IncusTarget) Units(ctx context.Context) ([]Unit, error) {
	out, err := t.exec(ctx, unitListArgv...)
	if err != nil {
		return nil, err
	}
	return ParseUnits(string(out)), nil
}

func (t *IncusTarget) WorkingDirectory(ctx context.Context, unit string) (string, error) {
	out, err := t.exec(ctx, workingDirectoryArgv(unit)...)
	if err != nil {
		return "", err
	}
	return parseWorkingDirectory(out)
}

func workingDirectoryArgv(unit string) []string {
	return []string{"systemctl", "show", "-p", "WorkingDirectory", unit}
}

func parseWorkingDirectory(out []byte) (string, error) {
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "WorkingDirectory=")
	if !ok || dir == "" {
		return "", errors.New("unit has no WorkingDirectory")
	}
	return dir, nil
}

// ParseUnits reads `systemctl list-units --plain --no-legend` output: UNIT LOAD ACTIVE SUB DESCRIPTION.
func ParseUnits(out string) []Unit {
	var units []Unit
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) < 4 || !runnerUnit.MatchString(fields[0]) {
			continue
		}
		units = append(units, Unit{Name: fields[0], Active: fields[2], Sub: fields[3]})
	}
	return units
}

type Container struct {
	Name      string
	Project   string
	Status    string
	CPUUsage  time.Duration
	MemBytes  int64
	Processes int
}

func IncusAvailable() bool {
	_, err := exec.LookPath("incus")
	return err == nil
}

func ListContainers(ctx context.Context) ([]Container, error) {
	out, err := run(ctx, nil, []string{"incus", "list", "-f", "json"})
	if err != nil {
		return nil, err
	}
	return ParseIncusList(out)
}

func ParseIncusList(data []byte) ([]Container, error) {
	var instances []struct {
		Name    string `json:"name"`
		Project string `json:"project"`
		Status  string `json:"status"`
		Type    string `json:"type"`
		State   *struct {
			CPU struct {
				Usage int64 `json:"usage"`
			} `json:"cpu"`
			Memory struct {
				Usage int64 `json:"usage"`
			} `json:"memory"`
			Processes int `json:"processes"`
		} `json:"state"`
	}
	if err := json.Unmarshal(data, &instances); err != nil {
		return nil, fmt.Errorf("incus list: %w", err)
	}
	containers := make([]Container, 0, len(instances))
	for _, instance := range instances {
		c := Container{Name: instance.Name, Project: instance.Project, Status: instance.Status}
		if instance.State != nil {
			c.CPUUsage = time.Duration(instance.State.CPU.Usage)
			c.MemBytes = instance.State.Memory.Usage
			c.Processes = instance.State.Processes
		}
		containers = append(containers, c)
	}
	return containers, nil
}

func GHToken(ctx context.Context) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	out, err := run(ctx, nil, []string{"gh", "auth", "token"})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func checkPatterns(fixed []string) error {
	for _, pattern := range fixed {
		if !slices.Contains(GrepPatterns, pattern) {
			return fmt.Errorf("grep pattern %q not allowed", pattern)
		}
	}
	return nil
}

func run(ctx context.Context, slots chan struct{}, argv []string) ([]byte, error) {
	if err := Check(argv); err != nil {
		return nil, err
	}
	if slots != nil {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s: timeout", describe(argv))
	}
	var exitErr *exec.ExitError
	isGrep := len(argv) > 4 && argv[4] == "grep"
	if isGrep && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil // grep found nothing
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v %s", describe(argv), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func describe(argv []string) string {
	if len(argv) > 4 && argv[0] == "incus" && argv[1] == "exec" {
		return "incus exec " + argv[2] + " " + argv[4]
	}
	return strings.Join(argv[:min(2, len(argv))], " ")
}

func nonEmptyLines(s string) []string {
	var lines []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
	}
	return lines
}
