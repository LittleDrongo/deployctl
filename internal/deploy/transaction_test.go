package deploy

import (
	"crypto/sha256"
	"fmt"
	"github.com/LittleDrongo/deployctl/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		p := `C:\Program Files\Git\bin\bash.exe`
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	return p
}

// Exercise the generated script and actual file replacement, rather than just
// asserting shell fragments. Only Docker, flock and the clock are simulated.
const transactionMock = `
flock() { return 0; }
date() { cat clock; }
sleep() { now=$(cat clock); echo "$((now+$1))" > clock; }
docker() {
 printf '%s\n' "$*" >> calls
 case "$1" in
 info) return 0 ;;
 container)
  [ "$2" = ls ] || return 92
  [ "$FAIL" != list ] || return 42
  for f in containers/*; do [ ! -f "$f" ] || basename "$f"; done ;;
 inspect)
  kind=$(cat "containers/$4") || return 1
  case "$3" in
   *State.Status*) echo 'exited exit=1 error= oom=false restarts=3' ;;
   *AutoRemove*) echo false ;;
   *deployctl.release*) echo "$RELEASE" ;;
   *deployctl.runtime*)
    if [ "$FAIL" = ssh-runtime ]; then echo ssh-home-container-v2;
    elif [ "$FAIL" != legacy-runtime ]; then echo image-container-v2; fi ;;
   *deployctl.binary*) echo "$OLD_HASH" ;;
   *State.Health*)
    if [ "$kind" = old ]; then echo "true false $OLD_HEALTH";
    elif [ "$FAIL" = unhealthy ]; then echo 'true false unhealthy';
    elif [ "$FAIL" = dsn ] || [ "$FAIL" = panic ] || [ "$FAIL" = silent ] || [ "$FAIL" = logfail ]; then echo 'false false none';
    elif [ "$FAIL" = loop ]; then echo 'true true none';
    elif [ "$FAIL" = timeout ]; then echo 'true false starting';
    elif [ "$FAIL" = slow ] && [ "$(cat clock)" -lt 115 ]; then echo 'true false starting';
    elif [ "$FAIL" = stable ]; then echo 'true false none';
    elif [ "$FAIL" = crash ] && [ "$(cat clock)" -lt 102 ]; then echo 'true false none';
    elif [ "$FAIL" = crash ]; then echo 'false false none';
    else echo 'true false healthy'; fi ;;
   *State.Running*) echo true ;;
   *) return 93 ;;
  esac ;;
 logs)
  case "$FAIL" in
   dsn) echo 'некорректный DSN database password=topsecret' >&2 ;;
   panic) echo 'panic: application failed' ;;
   loop) echo 'application is restarting' >&2 ;;
   silent) : ;;
   logfail) return 42 ;;
   *) echo 'application failed' ;;
  esac ;;
 build) cat >/dev/null; [ "$FAIL" != build ] ;;
 create)
  [ "$FAIL" != create ] || { echo 'invalid runtime argument' >&2; return 42; }
  while [ "$#" -gt 0 ]; do
   if [ "$1" = --name ]; then shift; created=$1; fi
   shift
  done
  echo new > "containers/$created" ;;
 stop)
  [ "$FAIL" != stop ] || return 42
  touch stopped ;;
 rm)
  for arg in "$@"; do last=$arg; done
  [ "$FAIL" != remove ] || return 42
  rm -- "containers/$last" ;;
 rename)
  if [ "$FAIL" = rename ] && [ "$2" = app ]; then return 42; fi
  mv -- "containers/$2" "containers/$3" ;;
 start)
  kind=$(cat "containers/$2") || return 1
  if [ "$FAIL" = start ] && [ "$kind" = new ]; then return 42; fi
  if [ "$FAIL" = signal ] && [ "$kind" = new ]; then kill -TERM $$; return 0; fi
  if [ "$FAIL" = rollback ] && [ "$kind" = new ]; then return 42; fi
  if [ "$FAIL" = rollback ] && [ "$kind" = old ]; then return 43; fi
  echo "$kind" >> started ;;
 restart) echo restart >> started ;;
 image)
  case "$2" in ls) return 0 ;; *) return 94 ;; esac ;;
 *) return 95 ;;
 esac
}
`

func TestDeploymentTransaction(t *testing.T) {
	for _, tc := range []struct {
		name, fail, release, health string
		old, wantError, wantNew     bool
	}{
		{"success", "", "previous", "none", true, false, true},
		{"first deployment", "", "previous", "none", false, false, true},
		{"already running", "", "same", "none", true, false, false},
		{"already healthy", "", "same", "healthy", true, false, false},
		{"upgrade runtime for same release", "legacy-runtime", "same", "healthy", true, false, true},
		{"switch SSH user to image for same release", "ssh-runtime", "same", "healthy", true, false, true},
		{"create error", "create", "previous", "none", true, true, false},
		{"build error", "build", "previous", "none", true, true, false},
		{"start rollback", "start", "previous", "none", true, true, false},
		{"unhealthy rollback", "unhealthy", "previous", "none", true, true, false},
		{"timeout rollback", "timeout", "previous", "none", true, true, false},
		{"slow startup", "slow", "previous", "none", true, false, true},
		{"stable process", "stable", "previous", "none", true, false, true},
		{"early process exit", "crash", "previous", "none", true, true, false},
		{"signal rollback", "signal", "previous", "none", true, true, false},
		{"stop error", "stop", "previous", "none", true, true, false},
		{"rename error", "rename", "previous", "none", true, true, false},
		{"list error", "list", "previous", "none", true, true, false},
		{"first deployment failure", "start", "previous", "none", false, true, false},
		{"failed recovery preserves backup", "rollback", "previous", "none", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, p := range []string{"service/.stage", "containers"} {
				if err := os.MkdirAll(filepath.Join(dir, p), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, v string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, p), []byte(v), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write("clock", "100\n")
			write("service/.stage/app.new", "new binary")
			if tc.old {
				write("service/app", "old binary")
				write("containers/app", "old\n")
			}
			target := target{Name: "prod", Target: config.Target{Container: "app", RemoteDir: "./service", Binary: "app", Image: "app:dev", Base: "alpine:3.20", ReadyTimeout: 20, ReadyInterval: 1, ReadyStable: 3, Mounts: []config.Mount{{HostPath: "./service", ContainerPath: "/app"}}}}
			script := transactionMock + deployScript(target, "./service/.stage", fmt.Sprintf("%x", sha256.Sum256([]byte("new binary"))), "same")
			script = strings.ReplaceAll(script, "/tmp/deployctl-lock-", "./locks/")
			write("test.sh", script)
			cmd := exec.Command(testShell(t), "test.sh")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "FAIL="+tc.fail, "RELEASE="+tc.release, "OLD_HEALTH="+tc.health, fmt.Sprintf("OLD_HASH=%x", sha256.Sum256([]byte("old binary"))))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, output=%s", err, out)
			}
			read := func(p string) string { b, _ := os.ReadFile(filepath.Join(dir, p)); return strings.TrimSpace(string(b)) }
			if tc.wantNew {
				if read("service/app") != "new binary" || read("containers/app") != "new" {
					t.Fatalf("new service missing: %s", out)
				}
			} else if tc.old {
				if read("service/app") != "old binary" || read("containers/app") != "old" {
					t.Fatalf("previous service lost: %s", out)
				}
			} else {
				if read("service/app") != "" || read("containers/app") != "" {
					t.Fatal("failed initial deployment left active files")
				}
			}
			if tc.release == "same" && !tc.wantNew {
				if !strings.Contains(string(out), "deployctl restart prod") {
					t.Fatalf("missing restart hint: %s", out)
				}
				for _, action := range []string{"stop ", "create ", "build ", "start ", "rename "} {
					if strings.Contains(read("calls"), action) {
						t.Fatalf("no-op mutated service: %s", read("calls"))
					}
				}
			}
			if tc.fail == "rollback" && read("service/.stage/previous") != "old binary" {
				t.Fatal("lost recovery backup")
			}
			if tc.fail == "build" && (!strings.Contains(string(out), "docker save -o deployctl-base-image.tar alpine:3.20") || !strings.Contains(string(out), "docker load -i deployctl-base-image.tar")) {
				t.Fatalf("missing offline base-image instructions: %s", out)
			}
			if tc.fail == "start" && tc.old && read("started") != "old" {
				t.Fatalf("old container was not restarted: %s", out)
			}
			if tc.fail == "slow" && read("clock") != "115" {
				t.Fatalf("did not wait for slow healthcheck: %s", out)
			}
			if tc.fail == "stable" && read("clock") != "103" {
				t.Fatalf("did not wait for stable process: %s", out)
			}
		})
	}
}
