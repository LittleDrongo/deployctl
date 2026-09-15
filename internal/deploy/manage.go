package deploy

func manageScript(t target, action string) string {
	s := lockScript(t) + "phase=" + action + "\ncontainer=" + quote(t.Container) + "\n"
	if action == "restart" {
		return s + `if container_exists "$container"; then :; else
 code=$?; [ "$code" != 1 ] || echo 'container does not exist; restart cannot create it' >&2
 exit "$code"
fi
` + diagnosticFunction + `trap 'code=$?; trap - EXIT; if [ "$code" != 0 ]; then diagnose "$container"; fi; exit "$code"' EXIT
docker restart "$container" >/dev/null
` + readinessScript(t)
	}
	s += `if container_exists "$container"; then
 docker stop "$container" >/dev/null
`
	if action == "remove" {
		s += " docker rm \"$container\" >/dev/null\n"
	}
	return s + "else\n code=$?; [ \"$code\" = 1 ] || exit \"$code\"\nfi\n"
}

// Capture the failed instance before rollback removes or renames any container.
// Logging failures never prevent recovery. Transport redacts both output streams.
const diagnosticFunction = `diagnose() {
 echo 'remote : Диагностика неудачного контейнера'
 docker inspect --format '{{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}} oom={{.State.OOMKilled}} restarts={{.RestartCount}}' "$1" 2>&1 || echo 'remote : Не удалось получить состояние контейнера'
 log_code=0
 logs=$(docker logs --tail 100 "$1" 2>&1) || log_code=$?
 if [ "$log_code" != 0 ]; then
  echo 'remote : Не удалось получить логи приложения'
 elif [ -n "$logs" ]; then
  printf '%s\n' "$logs"
 else
  echo 'remote : Логи приложения отсутствуют'
 fi
 return 0
}
`
