package completion

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCandidates(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod": "module example.com/app\n",
		"custom.yaml": `version: 1
defaults:
  build_target: linux-amd64
  remote_dir: /srv/app
  binary_name: app
  docker_container: app
  docker_image: app
  docker_base_image: alpine:3.20
  docker_mounts:
    - host_path: /srv/app
      container_path: /app
targets:
  prod:
    host: prod
  preview:
    host: preview
  test:
    host: test
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"st"}, "status\nstop\n"},
		{[]string{"up", "--k"}, "--keep-artifact\n"},
		{[]string{"up", "--config", "custom.yaml", "p"}, "preview\nprod\n"},
		{[]string{"up", "--root=" + root, "--config=custom.yaml", "p"}, "preview\nprod\n"},
		{[]string{"up", "--config", "missing.yaml", "p"}, ""},
		{[]string{"up", "--root", "p"}, ""},
		{[]string{"up", "prod", "p"}, ""},
		{[]string{"build", "--platform", "linux-a"}, "linux-amd64\nlinux-arm64\n"},
		{[]string{"config", "c"}, "check\n"},
	} {
		var out bytes.Buffer
		if err := Complete(tc.args, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want {
			t.Fatalf("%v: got %q want %q", tc.args, out.String(), tc.want)
		}
	}
}

func TestInstallCompletion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	var out bytes.Buffer
	for i := 0; i < 2; i++ {
		if err := Run([]string{"install", "bash"}, &out); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(root, "bash-completion", "completions", "deployctl")
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != Bash {
		t.Fatalf("%v %s", err, data)
	}
	if !strings.Contains(out.String(), "source '") {
		t.Fatal(out.String())
	}
	if err := os.WriteFile(dest, []byte("user completion"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"install", "bash"}, &out); err == nil {
		t.Fatal("overwrote user file")
	}
}

func TestBashBridge(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		p := `C:\Program Files\Git\bin\bash.exe`
		if _, statErr := os.Stat(p); statErr == nil {
			shell = p
			err = nil
		}
	}
	if err != nil {
		t.Skip("bash unavailable")
	}
	script := Bash + `
deployctl() {
 [[ "$1" == __complete && "$2" == up && "$3" == --root && "$4" == '/path with spaces' && "$5" == p ]] || return 1
 printf '%s\n' preview prod
}
COMP_WORDS=(deployctl up --root '/path with spaces' p)
COMP_CWORD=4
_deployctl_complete
[[ "${#COMPREPLY[@]}" == 2 && "${COMPREPLY[0]}" == preview && "${COMPREPLY[1]}" == prod ]]
`
	cmd := exec.Command(shell, "-c", script)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
}
