package deploy

import (
	"fmt"
	"path"
	"strings"
)

// Linux flock locks directory inodes. Descriptors are released by the kernel
// on exit; persistent empty directories are not stale locks. Directory locking
// also protects aliases of remote_dir which resolve to the same inode.
func lockScript(t target) string {
	return `set -eu
umask 077
command -v flock >/dev/null || { echo 'flock is required on the server' >&2; exit 1; }
` + "mkdir -p -- " + remotePath(t.RemoteDir) + "\n" +
		"(umask 022; mkdir -p -- " + quote("/tmp/deployctl-lock-"+t.Container) + ")\n" +
		"exec 9< " + quote("/tmp/deployctl-lock-"+t.Container) + "\n" +
		"flock -n 9 || { echo 'another operation on this container is active' >&2; exit 1; }\n" +
		"exec 8< " + remotePath(t.RemoteDir) + "\n" +
		"flock -n 8 || { echo 'another operation in remote_dir is active' >&2; exit 1; }\n" +
		"docker info >/dev/null\n" +
		`phase=preflight
trap 'code=$?; if [ "$code" != 0 ]; then printf "deployctl: stage %s failed (exit %s)\n" "$phase" "$code" >&2; fi' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
container_exists() {
 names=$(docker container ls -a --format '{{.Names}}') || return 2
 printf '%s\n' "$names" | grep -Fx -- "$1" >/dev/null
}
`
}

const stateFormat = "{{.State.Running}} {{.State.Restarting}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}"

func stateCommand(t target) string {
	return "docker inspect --format " + quote(stateFormat) + " " + quote(t.Container)
}

func readinessScript(t target) string {
	timeout, interval, stable := t.ReadyTimeout, t.ReadyInterval, t.ReadyStable
	if timeout == 0 {
		timeout = 120
	}
	if interval == 0 {
		interval = 2
	}
	if stable == 0 {
		stable = 5
	}
	return fmt.Sprintf(`phase=readiness
ready=0
healthy_since=0
deadline=$(( $(date +%%s) + %d ))
while :; do
 state=$(%s)
 now=$(date +%%s)
 case "$state" in
  'true false healthy') ready=1; break ;;
  'true false none')
   [ "$healthy_since" != 0 ] || healthy_since=$now
   if [ "$((now-healthy_since))" -ge %d ]; then ready=1; break; fi ;;
  'true false starting') healthy_since=0 ;;
  *) echo 'container exited, is restarting or unhealthy' >&2; break ;;
 esac
 [ "$now" -lt "$deadline" ] || break
 delay=%d
 remaining=$((deadline-now))
 [ "$delay" -le "$remaining" ] || delay=$remaining
 sleep "$delay"
done
[ "$ready" = 1 ] || { echo 'container readiness failed or timed out' >&2; exit 1; }
`, timeout, stateCommand(t), stable, interval) + progress("Контейнер готов; без healthcheck проверяется только стабильность процесса")
}

func runtimeScript(t target) string {
	var b strings.Builder
	b.WriteString("phase=mounts\n")
	for _, m := range t.Mounts {
		p := remotePath(m.HostPath)
		switch m.Create {
		case "dir":
			fmt.Fprintf(&b, "mkdir -p -- %s\ntest -d %s\n", p, p)
		case "file":
			fmt.Fprintf(&b, "mkdir -p -- %s\n[ ! -e %s ] || test -f %s\ntouch -- %s\n", remotePath(path.Dir(m.HostPath)), p, p, p)
		default:
			fmt.Fprintf(&b, "test -f %s || test -d %s\n", p, p)
		}
	}
	b.WriteString("phase=image\n" + progress("Подготовка образа "+t.Image))
	// WORKDIR consumes the rest of the line; paths cannot contain newlines.
	fmt.Fprintf(&b, "if ! docker build --label %s -t %s - > /dev/null <<'DEPLOY_DOCKERFILE'\nFROM %s\nWORKDIR %s\n%sDEPLOY_DOCKERFILE\nthen\n%s exit 1\nfi\n", quote("deployctl.repository="+imageRepository(t.Image)), quote(t.Image), t.Base, path.Dir(t.executable()), runtimeEnvironment(t), baseImageHelp(t.Base))
	return b.String()
}

// A numeric SSH identity need not have a home directory in the base image.
// Image defaults allow both --env and --env-file to override these paths.
func runtimeEnvironment(t target) string {
	if !useSSHUser(t) {
		return ""
	}
	home := path.Dir(t.executable())
	var b strings.Builder
	for _, env := range []struct{ key, value string }{
		{"HOME", home},
		{"XDG_CONFIG_HOME", path.Join(home, ".config")},
		{"XDG_CACHE_HOME", path.Join(home, ".cache")},
		{"XDG_DATA_HOME", path.Join(home, ".local/share")},
		{"XDG_STATE_HOME", path.Join(home, ".local/state")},
	} {
		// Dockerfile ENV has its own quoting and variable expansion rules.
		value := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(env.value)
		fmt.Fprintf(&b, "ENV %s=\"%s\"\n", env.key, value)
	}
	return b.String()
}

func runtimePolicy(t target) string {
	if useSSHUser(t) {
		return "ssh-home-container-v2"
	}
	return "image-container-v2"
}

// Create before touching the old container: Docker validates its configuration
// and mounts first. Starting separately lets rollback retain the original name.
func createContainer(t target, name, release, hash string) string {
	var b strings.Builder
	b.WriteString("deploy_container_host=$(docker info --format '{{.Name}}' 2>/dev/null || true)\ndeploy_container_user=$(id -un 2>/dev/null || true)\n")
	if useSSHUser(t) {
		b.WriteString("deploy_uid=$(id -u)\ndeploy_gid=$(id -g)\n")
	}
	fmt.Fprintf(&b, "docker create %s", words(t.RunArgs...))
	b.WriteString(` --env BUILDCARD_CONTAINER=true --env "BUILDCARD_CONTAINER_HOST=$deploy_container_host" --env "BUILDCARD_CONTAINER_USER=$deploy_container_user"`)
	fmt.Fprintf(&b, " --env %s --env %s", quote("BUILDCARD_CONTAINER_NAME="+t.Container), quote("BUILDCARD_CONTAINER_IMAGE="+t.Image))
	if useSSHUser(t) {
		b.WriteString(` --user "$deploy_uid:$deploy_gid"`)
	}
	for _, m := range t.Mounts {
		fmt.Fprintf(&b, " --mount %s%s%s", quote("type=bind,src="), remotePath(m.HostPath), quote(",dst="+m.ContainerPath))
	}
	fmt.Fprintf(&b, " --name %s --label %s --label %s --label %s --entrypoint %s %s", quote(name), quote("deployctl.release="+release), quote("deployctl.binary="+hash), quote("deployctl.runtime="+runtimePolicy(t)), quote(t.executable()), quote(t.Image))
	for _, a := range t.StartArgs {
		fmt.Fprintf(&b, " %s", quote(a.Key+"="+a.Value))
	}
	b.WriteString(" > /dev/null\n")
	return b.String()
}

func deployScript(t target, stage, hash, release string) string {
	binary := remotePath(t.RemoteDir + "/" + t.Binary)
	staged := remotePath(stage + "/" + t.Binary + ".new")
	backup := remotePath(stage + "/previous")
	candidate := t.Container + "-" + strings.TrimPrefix(path.Base(stage), ".") + "-new"
	previous := t.Container + "-" + strings.TrimPrefix(path.Base(stage), ".") + "-old"
	c := quote(t.Container)
	restart := "deployctl restart <target>"
	if t.Name != "" {
		restart = "deployctl restart " + t.Name
	}
	s := lockScript(t) + "phase=checksum\n" +
		"test \"$(sha256sum " + staged + " | cut -d ' ' -f 1)\" = " + quote(hash) + "\n" +
		"if [ -f " + binary + " ] && [ \"$(docker inspect --format '{{index .Config.Labels \"deployctl.release\"}}' " + c + " 2>/dev/null || true)\" = " + quote(release) + " ] &&\n" +
		"   [ \"$(docker inspect --format '{{index .Config.Labels \"deployctl.runtime\"}}' " + c + " 2>/dev/null || true)\" = " + quote(runtimePolicy(t)) + " ] &&\n" +
		"   [ \"$(sha256sum " + binary + " | cut -d ' ' -f 1)\" = \"$(docker inspect --format '{{index .Config.Labels \"deployctl.binary\"}}' " + c + " 2>/dev/null || true)\" ]; then\n" +
		" state=$(" + stateCommand(t) + ")\n case \"$state\" in 'true false none'|'true false healthy')\n" +
		progress("Эта версия уже запущена. Если нужен перезапуск, выполните "+restart+".") + "exit 0 ;; esac\nfi\n" +
		imageSnapshot(t) + runtimeScript(t) +
		"phase=backup\n" +
		"previous=" + quote(previous) + "\ncandidate=" + quote(candidate) + "\ncontainer=" + c + "\nbinary=" + binary + "\nbackup=" + backup + "\n" +
		`had_container=0
was_running=false
had_binary=0
committed=0
touched=0
renamed=0
promoted=0
replaced=0
if container_exists "$container"; then
 had_container=1
 was_running=$(docker inspect --format '{{.State.Running}}' "$container")
 auto_remove=$(docker inspect --format '{{.HostConfig.AutoRemove}}' "$container")
 [ "$auto_remove" = false ] || { echo 'existing container uses --rm; rollback cannot preserve it' >&2; exit 1; }
else
 code=$?; [ "$code" = 1 ] || exit "$code"
fi
if [ -e "$binary" ]; then
 test -f "$binary"
 cp -p -- "$binary" "$backup"
 had_binary=1
fi
[ "$had_container" = 0 ] || [ "$had_binary" = 1 ] || { echo 'existing container has no recoverable binary' >&2; exit 1; }
for reserved in "$previous" "$candidate"; do
 if container_exists "$reserved"; then echo 'temporary container name already exists' >&2; exit 1; else code=$?; [ "$code" = 1 ] || exit "$code"; fi
done
` + diagnosticFunction + `rollback() {
 code=$?
 trap - EXIT INT TERM HUP
 if [ "$committed" = 0 ]; then
  set +e
  failed=0
  if [ "$promoted" = 1 ]; then diagnose "$container"; elif [ "$phase" = create ]; then diagnose "$candidate"; fi
  docker rm -f "$candidate" >/dev/null 2>&1
  if [ "$promoted" = 1 ]; then
   if container_exists "$container"; then docker rm -f "$container" >/dev/null || failed=1; else check=$?; [ "$check" = 1 ] || failed=1; fi
  fi
  if [ "$replaced" = 1 ]; then
   if [ "$had_binary" = 1 ]; then
    cp -p -- "$backup" "$backup.restore" && mv -f -- "$backup.restore" "$binary" || failed=1
   else
    rm -f -- "$binary" || failed=1
   fi
  fi
  if [ "$renamed" = 1 ]; then
   if container_exists "$previous"; then docker rename "$previous" "$container" || failed=1; else check=$?; [ "$check" = 1 ] || failed=1; fi
  fi
  if [ "$touched" = 1 ] && [ "$was_running" = true ] && [ "$failed" = 0 ]; then
   docker start "$container" >/dev/null || failed=1
  fi
  if [ "$failed" = 0 ]; then
   rm -f -- "$backup"
   printf 'remote : Обновление отменено; предыдущее состояние восстановлено\n'
  else
   printf 'remote : Откат не завершён. Резервный бинарник: %s; проверьте контейнеры %s и %s\n' "$backup" "$previous" "$container" >&2
  fi
  printf 'deployctl: stage %s failed (exit %s)\n' "$phase" "$code" >&2
 fi
 exit "$code"
}
trap rollback EXIT
phase=create
` + createContainer(t, candidate, release, hash) +
		"chmod 755 -- " + staged + "\nphase=replace\n" +
		`if [ "$had_container" = 1 ]; then
 touched=1
 docker stop "$container" >/dev/null
 renamed=1
 docker rename "$container" "$previous"
fi
` + "replaced=1\nmv -f -- " + staged + " \"$binary\"\n" +
		`promoted=1
docker rename "$candidate" "$container"
phase=start
docker start "$container" >/dev/null
` + readinessScript(t) +
		`committed=1
phase=cleanup
if [ "$had_container" = 1 ]; then
 if ! docker rm "$previous" >/dev/null; then echo 'remote : Новый сервис работает; резервный контейнер не удалён' >&2; fi
fi
if ! rm -f -- "$backup"; then echo 'remote : Новый сервис работает; резервный бинарник не удалён' >&2; fi
` + "(\n" + cleanupImages(t) + "\n) || echo 'remote : Сервис работает; очистка образов не завершена' >&2\n"
	return s
}

// Explicit Docker arguments take precedence over the default SSH identity.
func useSSHUser(t target) bool {
	if t.ContainerUser == "image" {
		return false
	}
	for _, arg := range t.RunArgs {
		if arg == "--user" || arg == "-u" || strings.HasPrefix(arg, "--user=") || strings.HasPrefix(arg, "-u=") {
			return false
		}
	}
	return true
}
