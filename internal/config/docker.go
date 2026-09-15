package config

import (
	"fmt"
	"regexp"
	"strings"
)

var repositoryComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var dockerTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var registryName = regexp.MustCompile(`^(?:[a-zA-Z0-9]+(?:[.-][a-zA-Z0-9]+)*|\[[0-9a-fA-F:]+\])(?::[0-9]+)?$`)
var imageDigest = regexp.MustCompile(`^(?:sha256:[a-fA-F0-9]{64}|sha512:[a-fA-F0-9]{128})$`)

func validImage(ref string, allowDigest bool) bool {
	if ref == "" || len(ref) > 512 {
		return false
	}
	if strings.Contains(ref, "@") {
		parts := strings.Split(ref, "@")
		if !allowDigest || len(parts) != 2 || !imageDigest.MatchString(parts[1]) {
			return false
		}
		ref = parts[0]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		if !dockerTag.MatchString(ref[colon+1:]) {
			return false
		}
		ref = ref[:colon]
	}
	if len(ref) > 255 {
		return false
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost" || strings.HasPrefix(parts[0], "[")) {
		if !registryName.MatchString(parts[0]) {
			return false
		}
		parts = parts[1:]
	}
	for _, p := range parts {
		if !repositoryComponent.MatchString(p) {
			return false
		}
	}
	return true
}

// An explicit grammar keeps values separate from flags and rejects positional
// image/command arguments before an existing container is stopped.
func validateRunArgs(args []string) error {
	values := strings.Fields("--restart --network --network-alias --hostname -h --domainname --user -u --workdir -w --env -e --env-file --publish -p --expose --label -l --add-host --dns --dns-search --dns-option --cap-add --cap-drop --security-opt --memory -m --memory-swap --memory-reservation --cpus --cpu-shares -c --cpuset-cpus --cpuset-mems --pids-limit --ulimit --shm-size --group-add --log-driver --log-opt --health-cmd --health-interval --health-timeout --health-retries --health-start-period --health-start-interval --stop-signal --stop-timeout --runtime --gpus --ipc --pid --uts --cgroupns --pull --platform")
	bools := strings.Fields("--init --read-only --privileged --publish-all -P --no-healthcheck --oom-kill-disable")
	known := map[string]bool{}
	for _, k := range values {
		known[k] = true
	}
	for _, k := range bools {
		known[k] = false
	}
	for i := 0; i < len(args); i++ {
		key, val, equal := strings.Cut(args[i], "=")
		needsValue, ok := known[key]
		if !ok {
			return fmt.Errorf("unsupported or reserved docker_run_args option at entry %d; use separate option/value entries", i+1)
		}
		if needsValue {
			if !equal {
				i++
				if i == len(args) || strings.HasPrefix(args[i], "-") {
					return fmt.Errorf("docker_run_args option %s requires a value", key)
				}
				val = args[i]
			}
			if val == "" {
				return fmt.Errorf("docker_run_args option %s requires a nonempty value", key)
			}
			if (key == "--label" || key == "-l") && strings.HasPrefix(val, "deployctl.") {
				return fmt.Errorf("deployctl labels are reserved")
			}
		} else if equal && val != "true" && val != "false" {
			return fmt.Errorf("docker_run_args option %s requires true or false", key)
		}
	}
	return nil
}
