package qdc507

import (
	"context"
	"os"
	"testing"
)

func TestAbandonReleasesHostResourcesWithoutADB(t *testing.T) {
	snapshot := t.TempDir()
	adbCalls := 0
	runner := &scriptRunner{reply: func([]string) (string, error) {
		adbCalls++
		return "", nil
	}}
	runtime := &Runtime{snapshot: snapshot, runner: runner, transport: "7", cfg: Config{USBPath: "1-2"}}
	p := processRecord{Kind: kindRoute, PID: "123", StartTime: "456", Argv: append([]string(nil), routeArgv...), Created: true}
	runtime.track(p)
	if err := runtime.Abandon(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adbCalls != 0 {
		t.Fatal("abandoned runtime contacted the replacement device")
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("host snapshot was not removed: %v", err)
	}
	// The forgotten handle must not retain a claim that blocks a later owner.
	next := &Runtime{cfg: runtime.cfg, transport: runtime.transport}
	p.Created = false
	next.track(p)
	defer next.Abandon()
	if procs := next.Processes(); len(procs) != 1 || !procs[0].StopOnClose {
		t.Fatalf("old ownership was retained: %+v", procs)
	}
}
