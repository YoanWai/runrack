package target

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAllowsWhatTargetsBuild(t *testing.T) {
	inc := []string{"incus", "exec", "runner-acme-1", "--"}
	allowed := [][]string{
		{"incus", "list", "-f", "json"},
		{"gh", "auth", "token"},
		unitListArgv,
		workingDirectoryArgv("actions.runner.acme.zenbook-acme-1.service"),
		append(append([]string{}, inc...), unitListArgv...),
		append(append([]string{}, inc...), workingDirectoryArgv("actions.runner.acme-labs.zenbook.v2.service")...),
		append(append([]string{}, inc...), "head", "-c", "2097152", "/opt/actions-runner/_diag/Worker_20261123-174638-utc.log"),
		append(append([]string{}, inc...), "ls", "-1", "/opt/actions-runner/_diag"),
		append(append([]string{}, inc...), "grep", "-a", "-h", "-F", "-e", GrepPatterns[0], "-e", GrepPatterns[1], "/opt/actions-runner/_diag/Worker_1.log"),
	}
	for _, argv := range allowed {
		if err := Check(argv); err != nil {
			t.Errorf("Check(%q) = %v, want allowed", argv, err)
		}
	}
}

func TestCheckRejects(t *testing.T) {
	rejected := [][]string{
		{"systemctl", "stop", "actions.runner.acme.zenbook-acme-1.service"},
		{"systemctl", "restart", "actions.runner.acme.zenbook-acme-1.service"},
		{"incus", "exec", "c", "--", "rm", "-rf", "/opt/actions-runner"},
		{"incus", "exec", "c", "--", "sh", "-c", "head -c 1 /x"},
		{"incus", "exec", "c", "--", "cat", "/opt/actions-runner/.credentials"},
		{"incus", "exec", "c", "--", "head", "-c", "1", "/opt/actions-runner/.credentials"},
		{"incus", "exec", "c", "--", "head", "-c", "1", "/opt/actions-runner/.credentials_rsaparams"},
		{"incus", "exec", "c", "--", "head", "-c", "1", "/opt/actions-runner/.env"},
		{"incus", "exec", "c", "--", "head", "-c", "1", "/x/../y"},
		{"incus", "exec", "c", "--", "head", "-c", "1", "relative/path"},
		{"incus", "exec", "c", "--", "grep", "-a", "-h", "-F", "-e", "token", "/x.log"},
		{"incus", "exec", "c", "--", "grep", "-r", "-e", GrepPatterns[0], "/x"},
		{"incus", "exec", "c;reboot", "--", "ls", "-1", "/x"},
		{"incus", "stop", "runner-acme-1"},
		{"incus", "delete", "runner-acme-1"},
		{"head", "-c", "1", "/etc/hostname"},
		{"systemctl", "show", "-p", "WorkingDirectory", "ssh.service"},
		{"gh", "auth", "login"},
	}
	for _, argv := range rejected {
		if err := Check(argv); err == nil {
			t.Errorf("Check(%q) allowed, want rejected", argv)
		}
	}
}

func TestCheckPathOnHostReads(t *testing.T) {
	host := NewHost()
	if _, err := host.Head(t.Context(), "/srv/runners/x/.credentials", 10); err == nil {
		t.Fatal("host read of .credentials must be refused")
	}
	if _, err := host.Grep(t.Context(), "/srv/runners/x/_diag/Worker_1.log", "token"); err == nil {
		t.Fatal("host grep with a pattern outside the list must be refused")
	}
}

// Only this package may start processes. Everything else goes through a Target.
func TestNoExecOutsideTarget(t *testing.T) {
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasPrefix(filepath.ToSlash(path), "../../internal/target/") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" && strings.HasPrefix(sel.Sel.Name, "Command") {
				t.Errorf("%s: exec.%s outside internal/target", path, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseUnits(t *testing.T) {
	units := ParseUnits(readFixture(t, "systemctl/list-units.txt"))
	want := []Unit{
		{"actions.runner.acme.zenbook-acme-1.service", "active", "running"},
		{"actions.runner.acme.zenbook-acme-2.service", "failed", "failed"},
		{"actions.runner.acme-labs.zenbook.v2.service", "inactive", "dead"},
	}
	if len(units) != len(want) {
		t.Fatalf("got %d units: %+v", len(units), units)
	}
	for i := range want {
		if units[i] != want[i] {
			t.Errorf("unit %d = %+v, want %+v", i, units[i], want[i])
		}
	}
}

func TestParseIncusList(t *testing.T) {
	containers, err := ParseIncusList([]byte(readFixture(t, "incus/list.json")))
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 7 {
		t.Fatalf("got %d containers", len(containers))
	}
	if c := containers[0]; c.Name != "runner-base" || c.Status != "Stopped" {
		t.Fatalf("first = %+v", c)
	}
	if c := containers[6]; c.Name != "runner-acme-3" || c.Processes != 307 || c.MemBytes == 0 || c.CPUUsage == 0 {
		t.Fatalf("last = %+v", c)
	}
}
