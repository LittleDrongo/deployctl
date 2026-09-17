// Package config loads the versioned application configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const Filename = "config_deploy.yaml"
const LegacyFilename = "deploy.yaml"

type Build struct {
	Package   string   `yaml:"package"`
	Platform  string   `yaml:"platform"`
	Platforms []string `yaml:"platforms"`
}

type Mount struct {
	HostPath      string `yaml:"host_path"`
	ContainerPath string `yaml:"container_path"`
	Create        string `yaml:"create_host_path"`
}

type Argument struct{ Key, Value string }
type Arguments []Argument

func (a *Arguments) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("start_args must be a mapping")
	}
	*a = nil
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !strings.HasPrefix(k.Value, "--") || len(k.Value) < 3 || strings.ContainsAny(k.Value, "= \t\r\n") || seen[k.Value] || v.Kind != yaml.ScalarNode {
			return fmt.Errorf("start_args requires unique --keys and scalar values (line %d)", k.Line)
		}
		seen[k.Value] = true
		*a = append(*a, Argument{k.Value, v.Value})
	}
	return nil
}

// Port remains nil when omitted, allowing OpenSSH to resolve it from ssh_config.
type Target struct {
	ContainerUser string    `yaml:"container_user"`
	Host          string    `yaml:"host"`
	User          string    `yaml:"user"`
	Port          *int      `yaml:"port"`
	BuildTarget   string    `yaml:"build_target"`
	RemoteDir     string    `yaml:"remote_dir"`
	Binary        string    `yaml:"binary_name"`
	Image         string    `yaml:"docker_image"`
	Container     string    `yaml:"docker_container"`
	Base          string    `yaml:"docker_base_image"`
	RunArgs       []string  `yaml:"docker_run_args"`
	Mounts        []Mount   `yaml:"docker_mounts"`
	StartArgs     Arguments `yaml:"start_args"`
	ReadyTimeout  int       `yaml:"ready_timeout_seconds"`
	ReadyInterval int       `yaml:"ready_interval_seconds"`
	ReadyStable   int       `yaml:"ready_stable_seconds"`
	SSHTimeout    int       `yaml:"ssh_timeout_seconds"`
}

type Config struct {
	Version int
	Build   Build
	Targets map[string]Target
}

// Path resolves even an explicitly selected config relative to the module root.
func Path(root, filename string) string {
	if filename == "" {
		filename = Filename
		// Prefer the new name, but keep existing projects working unchanged.
		if _, err := os.Lstat(filepath.Join(root, Filename)); os.IsNotExist(err) {
			if _, legacyErr := os.Lstat(filepath.Join(root, LegacyFilename)); !os.IsNotExist(legacyErr) {
				filename = LegacyFilename
			}
		}
	}
	if filepath.IsAbs(filename) {
		return filepath.Clean(filename)
	}
	return filepath.Join(root, filename)
}

func Load(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", filename, err)
	}
	return c, nil
}

func strict(data []byte, value any) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	return d.Decode(value)
}

func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" || n.Tag == "!!null" {
		return fmt.Errorf("null, aliases and YAML merge keys are not supported (line %d)", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return fmt.Errorf("invalid or duplicate key at line %d", key.Line)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkNode(child); err != nil {
			return err
		}
	}
	return nil
}

func Parse(data []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(data))
	var tree yaml.Node
	if err := d.Decode(&tree); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("config must contain exactly one YAML document")
	}
	if err := checkNode(&tree); err != nil {
		return c, err
	}
	var raw struct {
		Version  int                  `yaml:"version"`
		Build    Build                `yaml:"build"`
		Defaults yaml.Node            `yaml:"defaults"`
		Targets  map[string]yaml.Node `yaml:"targets"`
	}
	if err := strict(data, &raw); err != nil {
		return c, err
	}
	if raw.Version != 1 {
		return c, fmt.Errorf("unsupported config version %d; expected version: 1", raw.Version)
	}
	if raw.Build.Package == "" {
		raw.Build.Package = "."
	}
	if err := ValidatePackage(raw.Build.Package); err != nil {
		return c, err
	}
	if raw.Build.Platform != "" && !platform.MatchString(raw.Build.Platform) {
		return c, fmt.Errorf("build.platform must be OS-ARCH")
	}
	if raw.Build.Platform != "" && len(raw.Build.Platforms) > 0 {
		return c, fmt.Errorf("use either build.platform or build.platforms, not both")
	}
	seenPlatforms := map[string]bool{}
	for _, value := range raw.Build.Platforms {
		if !platform.MatchString(value) {
			return c, fmt.Errorf("build.platforms entry %q must be OS-ARCH", value)
		}
		if seenPlatforms[value] {
			return c, fmt.Errorf("duplicate build.platforms entry %q", value)
		}
		seenPlatforms[value] = true
	}
	c.Version, c.Build, c.Targets = raw.Version, raw.Build, make(map[string]Target)
	// Check defaults even if no targets have been configured yet.
	base := Target{ReadyTimeout: 120, ReadyInterval: 2, ReadyStable: 5, SSHTimeout: 1200}
	if err := overlay(&raw.Defaults, &base); err != nil {
		return c, fmt.Errorf("defaults: %w", err)
	}
	if err := validate(base, false); err != nil {
		return c, fmt.Errorf("defaults: %w", err)
	}
	for name, node := range raw.Targets {
		if !simpleName.MatchString(name) {
			return c, fmt.Errorf("invalid target name %q", name)
		}
		// Decode defaults afresh: slices and pointer fields must not alias targets.
		t := Target{ReadyTimeout: 120, ReadyInterval: 2, ReadyStable: 5, SSHTimeout: 1200}
		if err := overlay(&raw.Defaults, &t); err != nil {
			return c, err
		}
		if err := overlay(&node, &t); err != nil {
			return c, fmt.Errorf("target %s: %w", name, err)
		}
		if err := validate(t, true); err != nil {
			return c, fmt.Errorf("target %s: %w", name, err)
		}
		c.Targets[name] = t
	}
	return c, nil
}

func overlay(n *yaml.Node, target *Target) error {
	if n.Kind == 0 {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping")
	}
	data, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	return strict(data, target)
}

var simpleName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var sshHost = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:-]*$`)
var platform = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

func ValidatePackage(value string) error {
	if value != "." && (!strings.HasPrefix(value, "./") || strings.Contains(value, "...") || strings.ContainsAny(value, "\\\x00\r\n") || path.Clean(value) == ".." || strings.HasPrefix(path.Clean(value), "../")) {
		return fmt.Errorf("build.package must be . or a relative package such as ./cmd/server")
	}
	return nil
}

func remotePath(p string) bool {
	return (strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~/")) && p != "/" && p != "~/" && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n,:")
}

func validate(t Target, complete bool) error {
	if t.ContainerUser != "" && t.ContainerUser != "ssh" && t.ContainerUser != "image" {
		return fmt.Errorf("container_user must be ssh or image")
	}
	if complete && (t.Host == "" || t.RemoteDir == "" || t.BuildTarget == "" || t.Binary == "" || t.Container == "" || t.Image == "" || t.Base == "") {
		return fmt.Errorf("host, remote_dir, build_target, binary_name, docker_container, docker_image and docker_base_image are required after defaults")
	}
	if t.Host != "" && !sshHost.MatchString(t.Host) {
		return fmt.Errorf("invalid SSH host or alias")
	}
	if t.User != "" && !simpleName.MatchString(t.User) {
		return fmt.Errorf("invalid SSH user")
	}
	if t.Port != nil && (*t.Port < 1 || *t.Port > 65535) {
		return fmt.Errorf("port must be 1..65535; omit it to use SSH config")
	}
	if t.BuildTarget != "" && t.BuildTarget != "linux-amd64" && t.BuildTarget != "linux-arm64" {
		return fmt.Errorf("build_target must be linux-amd64 or linux-arm64")
	}
	if t.RemoteDir != "" && !remotePath(t.RemoteDir) {
		return fmt.Errorf("remote_dir must be a normalized absolute or ~/ Linux path, not root")
	}
	for _, name := range []string{t.Binary, t.Container} {
		if name != "" && !simpleName.MatchString(name) {
			return fmt.Errorf("invalid binary or container name")
		}
	}
	if t.Image != "" && !validImage(t.Image, false) || t.Base != "" && !validImage(t.Base, true) {
		return fmt.Errorf("invalid Docker image reference")
	}
	if err := validateRunArgs(t.RunArgs); err != nil {
		return err
	}
	if t.ReadyTimeout < 1 || t.ReadyTimeout > 3600 || t.ReadyInterval < 1 || t.ReadyInterval > t.ReadyTimeout || t.ReadyStable < 1 || t.ReadyStable > t.ReadyTimeout || t.SSHTimeout < t.ReadyTimeout+30 || t.SSHTimeout > 86400 {
		return fmt.Errorf("invalid readiness or SSH timeouts")
	}
	seen := map[string]bool{}
	binaryPath := ""
	for _, m := range t.Mounts {
		if !remotePath(m.HostPath) || !strings.HasPrefix(m.ContainerPath, "/") || path.Clean(m.ContainerPath) != m.ContainerPath || strings.ContainsAny(m.ContainerPath, "\x00\r\n,:") {
			return fmt.Errorf("invalid mount path")
		}
		if seen[m.ContainerPath] {
			return fmt.Errorf("duplicate container mount %q", m.ContainerPath)
		}
		seen[m.ContainerPath] = true
		if m.Create != "" && m.Create != "dir" && m.Create != "file" {
			return fmt.Errorf("create_host_path must be dir or file")
		}
		if m.HostPath == t.RemoteDir {
			if m.Create == "file" || binaryPath != "" {
				return fmt.Errorf("remote_dir must be mounted once as a directory")
			}
			binaryPath = path.Join(m.ContainerPath, t.Binary)
		}
	}
	if complete && binaryPath == "" {
		return fmt.Errorf("docker_mounts must mount remote_dir as a directory")
	}
	for _, m := range t.Mounts {
		if binaryPath != "" && m.HostPath != t.RemoteDir && (m.ContainerPath == binaryPath || strings.HasPrefix(binaryPath, m.ContainerPath+"/")) {
			return fmt.Errorf("mount shadows service binary")
		}
	}
	for _, a := range t.RunArgs {
		if strings.ContainsAny(a, "\x00\r\n") {
			return fmt.Errorf("docker_run_args contains control characters")
		}
	}
	for _, a := range t.StartArgs {
		if strings.ContainsRune(a.Value, 0) {
			return fmt.Errorf("start_args contains NUL")
		}
	}
	return nil
}

// ValidateTarget checks the complete contract required before a remote operation.
func ValidateTarget(t Target) error { return validate(t, true) }
