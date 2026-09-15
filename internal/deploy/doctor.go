package deploy

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/LittleDrongo/deployctl/internal/config"
)

func doctorPathCheck(label, filename, kind string, mayCreate bool) string {
	return "check_path " + quote(label) + " " + remotePath(filename) + " " + words(kind, fmt.Sprint(mayCreate)) + "\n"
}

func baseImageHelp(base string) string {
	lines := []string{
		"С доступом к registry: docker pull " + base,
		"Без доступа к registry выполните на подключённой машине: docker pull " + base,
		"Затем: docker save -o deployctl-base-image.tar " + base,
		"Передайте архив по SCP и выполните на сервере: docker load -i deployctl-base-image.tar",
	}
	return "printf '%s\\n' " + words(lines...) + " >&2\n"
}

func doctorScript(t config.Target) string {
	var b strings.Builder
	b.WriteString(`set -u
failures=0
ok() { printf 'OK   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; failures=$((failures+1)); }
require_command() {
 if command -v "$1" >/dev/null 2>&1; then ok "command $1"; else fail "command $1 is missing"; fi
}
creatable_from_ancestor() {
 candidate=$1
 while [ ! -e "$candidate" ]; do
  parent=$(dirname -- "$candidate")
  [ "$parent" != "$candidate" ] || break
  candidate=$parent
 done
 [ -d "$candidate" ] && [ -w "$candidate" ] && [ -x "$candidate" ]
}
check_path() {
 label=$1
 filename=$2
 kind=$3
 may_create=$4
 if [ -e "$filename" ]; then
  case "$kind" in
   dir) [ -d "$filename" ] || { fail "$label must be a directory: $filename"; return; } ;;
   file) [ -f "$filename" ] || { fail "$label must be a regular file: $filename"; return; } ;;
  esac
  [ -r "$filename" ] || { fail "$label is not readable: $filename"; return; }
  if [ "$kind" = dir ]; then
   [ -x "$filename" ] || { fail "$label is not searchable: $filename"; return; }
  fi
  if [ "$may_create" = true ]; then
   [ -w "$filename" ] || { fail "$label is not writable: $filename"; return; }
  fi
  ok "$label: $filename"
 elif [ "$may_create" = true ] && creatable_from_ancestor "$filename"; then
  ok "$label can be created: $filename"
 else
  fail "$label is missing or cannot be created: $filename"
 fi
}

printf 'Remote user: %s (uid=%s gid=%s)\n' "$(id -un 2>/dev/null || printf unknown)" "$(id -u 2>/dev/null || printf unknown)" "$(id -g 2>/dev/null || printf unknown)"
for command_name in sh docker flock timeout sha256sum dirname id; do require_command "$command_name"; done
if command -v docker >/dev/null 2>&1; then
 if docker info >/dev/null 2>&1; then
  ok 'Docker daemon access'
`)
	fmt.Fprintf(&b, "  if docker image inspect %s >/dev/null 2>&1; then ok %s; else fail %s; %sfi\n", quote(t.Base), quote("base image "+t.Base), quote("base image is unavailable locally: "+t.Base), baseImageHelp(t.Base))
	b.WriteString(" else fail 'Docker daemon is unavailable or permission is denied'\n fi\nfi\n")
	b.WriteString(doctorPathCheck("remote_dir", t.RemoteDir, "dir", true))
	for i, mount := range t.Mounts {
		label := fmt.Sprintf("mount[%d]", i)
		kind := mount.Create
		mayCreate := kind != ""
		if mount.HostPath == t.RemoteDir {
			kind, mayCreate = "dir", true
		}
		b.WriteString(doctorPathCheck(label, mount.HostPath, kind, mayCreate))
	}
	b.WriteString(`if [ "$failures" -ne 0 ]; then
 printf 'Doctor found %s problem(s).\n' "$failures" >&2
 exit 1
fi
printf 'Doctor: environment is ready.\n'
`)
	return b.String()
}

// Doctor checks remote runtime prerequisites without creating directories,
// pulling images or changing containers. The CLI checks local SSH clients.
func Doctor(ctx context.Context, transport Transport, t config.Target, name string, dry bool, out io.Writer) error {
	if err := config.ValidateTarget(t); err != nil {
		return err
	}
	if dry {
		_, err := fmt.Fprintf(out, "План doctor %s на %s: проверить SSH/SCP, Docker, обязательные команды, базовый образ %s, remote_dir и %d монтирований.\nSSH не выполняется; сервер не изменяется.\n", name, t.Host, t.Base, len(t.Mounts))
		return err
	}
	return transport.Shell(ctx, t, doctorScript(t), "doctor")
}
