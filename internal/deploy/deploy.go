// Package deploy executes a remote deployment transaction using an explicit transport.
package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type Transport interface {
	Shell(context.Context, config.Target, string, string) error
	Upload(context.Context, config.Target, string, string) error
}

type target struct {
	config.Target
	Name string
}

func (t target) executable() string {
	for _, m := range t.Mounts {
		if m.HostPath == t.RemoteDir {
			return path.Join(m.ContainerPath, t.Binary)
		}
	}
	return ""
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func remotePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		return `"$HOME"/` + quote(p[2:])
	}
	return quote(p)
}
func words(values ...string) string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = quote(v)
	}
	return strings.Join(result, " ")
}
func progress(message string) string { return "printf '%s\\n' " + quote("remote : "+message) + "\n" }

var dockerVersion = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func VersionedImage(image, version string) (string, error) {
	if !dockerVersion.MatchString(version) {
		return "", fmt.Errorf("version %q cannot be represented as a Docker tag", version)
	}
	return imageRepository(image) + ":" + version, nil
}

func Up(ctx context.Context, transport Transport, t config.Target, name, artifact string, dry bool, out io.Writer) error {
	if err := config.ValidateTarget(t); err != nil {
		return err
	}
	if dry {
		_, err := fmt.Fprintf(out, "План up %s: %s → %s:%s/%s\nОбраз: %s; контейнер: %s\nSSH/SCP → SHA-256 → блокировки → резервная копия → запуск → readiness → очистка; при ошибке диагностика и откат.\nSSH не выполняется. Пользователь и порт берутся из OpenSSH, если не заданы в конфиге.\n", name, artifact, t.Host, t.RemoteDir, t.Binary, t.Image, t.Container)
		return err
	}
	file, err := os.Open(artifact)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() == 0 {
		file.Close()
		return fmt.Errorf("artifact must be a nonempty regular file")
	}
	h := sha256.New()
	_, err = io.Copy(h, file)
	file.Close()
	if err != nil {
		return err
	}
	// Resolve the default policy before hashing, so existing root containers
	// are redeployed after upgrading to the SSH-user default.
	if t.ContainerUser == "" {
		t.ContainerUser = "ssh"
	}
	hash := hex.EncodeToString(h.Sum(nil))
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	release := fmt.Sprintf("%x", sha256.Sum256(append([]byte(hash+"\x00"), raw...)))
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	stage := t.RemoteDir + "/.deploy-" + hex.EncodeToString(token)
	if err := transport.Shell(ctx, t, "set -eu\numask 077\nmkdir -p -- "+remotePath(t.RemoteDir)+"\nmkdir -- "+remotePath(stage)+"\n", "prepare"); err != nil {
		return err
	}
	wrapped := target{Target: t, Name: name}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		script := lockScript(wrapped) + "rm -f -- " + remotePath(stage+"/"+t.Binary+".new") + "\nif [ -f " + remotePath(stage+"/previous") + " ]; then\n" + progress("Резервный бинарник сохранён в "+stage+"/previous") + "else\n rmdir -- " + remotePath(stage) + "\nfi\n"
		if err := transport.Shell(cleanupCtx, t, script, "cleanup"); err != nil {
			fmt.Fprintf(out, "Не удалось очистить staging %s: %v\n", stage, err)
		}
	}()
	abs, err := filepath.Abs(artifact)
	if err != nil {
		return err
	}
	if err := transport.Upload(ctx, t, abs, stage+"/"+t.Binary+".new"); err != nil {
		return err
	}
	return transport.Shell(ctx, t, deployScript(wrapped, stage, hash, release), "up")
}

func Manage(ctx context.Context, transport Transport, t config.Target, action string, dry bool, out io.Writer) error {
	if action != "stop" && action != "restart" && action != "remove" {
		return fmt.Errorf("unknown management action %q", action)
	}
	if err := config.ValidateTarget(t); err != nil {
		return err
	}
	if dry {
		_, err := fmt.Fprintf(out, "План %s: контейнер %s на %s; SSH не выполняется.\n", action, t.Container, t.Host)
		return err
	}
	return transport.Shell(ctx, t, manageScript(target{Target: t}, action), action)
}
