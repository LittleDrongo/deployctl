package config

import (
	"strings"
	"testing"
)

const valid = `version: 1
build:
  package: ./cmd/server
defaults:
  build_target: linux-amd64
  remote_dir: /opt/service
  binary_name: service
  docker_container: service
  docker_image: service:latest
  docker_base_image: debian:bookworm-slim
  docker_run_args: [--network=host]
  docker_mounts:
    - host_path: /opt/service
      container_path: /app
  start_args:
    --port: 8080
    --debug: true
targets:
  one:
    host: first
  two:
    host: second
    port: 2222
    start_args: {}
    docker_run_args: []
`

func TestInheritanceAndSSHConfig(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	one, two := c.Targets["one"], c.Targets["two"]
	if one.Port != nil || one.User != "" || two.Port == nil || *two.Port != 2222 {
		t.Fatal("SSH config defaults overridden")
	}
	if len(one.StartArgs) != 2 || one.StartArgs[0].Key != "--port" || one.StartArgs[0].Value != "8080" || one.StartArgs[1].Value != "true" || len(two.StartArgs) != 0 || len(two.RunArgs) != 0 {
		t.Fatalf("incorrect merge: %+v %+v", one, two)
	}
	if c.Build.Package != "./cmd/server" || two.ReadyTimeout != 120 {
		t.Fatal("missing defaults")
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"reserved name":              strings.Replace(valid, "docker_run_args: [--network=host]", "docker_run_args: [--name=other]", 1),
		"auto remove":                strings.Replace(valid, "docker_run_args: [--network=host]", "docker_run_args: [--rm]", 1),
		"reserved label":             strings.Replace(valid, "docker_run_args: [--network=host]", "docker_run_args: [--label=deployctl.release=other]", 1),
		"image injection":            strings.Replace(valid, "debian:bookworm-slim", "debian;echo", 1),
		"version":                    strings.Replace(valid, "version: 1", "version: 2", 1),
		"unknown top":                valid + "unknown: true\n",
		"unknown build":              strings.Replace(valid, "package:", "pakage:", 1),
		"unknown defaults":           strings.Replace(valid, "binary_name:", "binary_nam:", 1),
		"unknown target":             strings.Replace(valid, "host: first", "host: first\n    typo: true", 1),
		"unknown mount":              strings.Replace(valid, "container_path:", "container_pat:", 1),
		"null":                       strings.Replace(valid, "port: 2222", "port: null", 1),
		"port":                       strings.Replace(valid, "port: 2222", "port: 0", 1),
		"document":                   valid + "---\nversion: 1\n",
		"duplicate":                  valid + "version: 1\n",
		"missing host":               strings.Replace(valid, "host: first", "user: app", 1),
		"unsafe path":                strings.ReplaceAll(valid, "/opt/service", "/"),
		"missing mount":              strings.Replace(valid, "container_path: /app", "container_path: relative", 1),
		"duplicate argument":         strings.Replace(valid, "--debug: true", "--port: 1234", 1),
		"alias":                      "version: 1\ndefaults: &d {}\ntargets: {one: *d}\n",
		"package escape":             strings.Replace(valid, "./cmd/server", "./../server", 1),
		"empty targets bad defaults": "version: 1\ndefaults: {port: 0}\ntargets: {}\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
