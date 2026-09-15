package deploy

import (
	"github.com/LittleDrongo/deployctl/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestImageCleanup(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
		_, err = os.Stat(bash)
	}
	if err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	target := target{Target: config.Target{Image: "registry:5000/app:dev", Container: "app"}}
	mock := `set -eu
docker() {
 case "$1 $2" in
 'container inspect') echo current ;;
 'container ls') case "$5" in ancestor=used|ancestor=busy) echo another-container ;; esac ;;
 'image ls')
   if [ "$3" = '-q' ]; then echo 'owned busy';
   elif [ "$5" = '{{.Repository}} {{.ID}}' ]; then
     printf '%s\n' 'registry:5000/app previous-dev' 'registry:5000/app foreign-tag' 'registry:5000/other unrelated'
   else
     printf '%s\n' 'registry:5000/app dev current' 'registry:5000/app v1 old' 'registry:5000/app v2 used' 'registry:5000/app v3 blocked' 'registry:5000/other dev unrelated'
   fi ;;
 'image inspect') case "$5" in foreign-tag) echo 1 ;; *) echo 0 ;; esac ;;
 'image rm')
   printf '%s\n' "$5" >> removed
   [ "$5" != 'registry:5000/app:v3' ] ;;
 *) return 99 ;;
 esac
}
`
	script := mock + imageSnapshot(target) + cleanupImages(target)
	if err := os.WriteFile(filepath.Join(dir, "test.sh"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bash, "test.sh")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "removed"))
	calls := string(data)
	for _, wanted := range []string{"registry:5000/app:v1", "previous-dev", "owned"} {
		if !strings.Contains(calls, wanted+"\n") {
			t.Errorf("missed %s: %s", wanted, calls)
		}
	}
	for _, protected := range []string{"current", "used", "busy", "unrelated", "foreign-tag"} {
		if strings.Contains(calls, protected) {
			t.Errorf("deleted protected %s: %s", protected, calls)
		}
	}
	if !strings.Contains(string(out), "Не удалось удалить") {
		t.Fatal("deletion failure not reported")
	}

}

func TestCleanupRunsAfterStartup(t *testing.T) {
	target := target{Target: config.Target{Image: "app:dev", Container: "app", RemoteDir: "/srv/app", Binary: "app", Base: "alpine:3.20", Mounts: []config.Mount{{HostPath: "/srv/app", ContainerPath: "/app"}}}}
	script := deployScript(target, "/srv/app/.stage", "hash", "release")
	if strings.Index(script, "current_image=$(") < strings.Index(script, "committed=1") {
		t.Fatal("cleanup before successful commit")
	}
}
