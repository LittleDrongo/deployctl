package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type observeTransport struct {
	shells, streams int
	script, phase   string
}

func (o *observeTransport) Shell(_ context.Context, _ config.Target, script, phase string) error {
	o.shells++
	o.script, o.phase = script, phase
	return nil
}
func (o *observeTransport) Stream(_ context.Context, _ config.Target, script, phase string) error {
	o.streams++
	o.script, o.phase = script, phase
	return nil
}
func (o *observeTransport) Upload(context.Context, config.Target, string, string) error { return nil }

func TestStatusAndLogsTransport(t *testing.T) {
	target := doctorTarget()
	transport := &observeTransport{}
	var out bytes.Buffer
	if err := Status(context.Background(), transport, target, "prod", true, &out); err != nil || transport.shells != 0 {
		t.Fatalf("status dry-run: %v calls=%d", err, transport.shells)
	}
	if err := Status(context.Background(), transport, target, "prod", false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.shells != 1 || transport.phase != "status" || !strings.Contains(transport.script, ".RestartCount") || !strings.Contains(transport.script, "Состояние: отсутствует") {
		t.Fatalf("status script: %+v", transport)
	}
	if err := Logs(context.Background(), transport, target, "prod", 250, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.shells != 2 || !strings.Contains(transport.script, "docker logs --tail '250' 'app'") {
		t.Fatalf("tail script: %+v", transport)
	}
	if err := Logs(context.Background(), transport, target, "prod", 10, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.streams != 1 || transport.phase != "logs --follow" || !strings.Contains(transport.script, "--follow 'app'") {
		t.Fatalf("follow script: %+v", transport)
	}
	if err := Logs(context.Background(), transport, target, "prod", 0, false, false, &out); err == nil {
		t.Fatal("invalid tail accepted")
	}
}
