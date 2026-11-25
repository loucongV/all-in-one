package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestSummarizeActiveRunsForStop(t *testing.T) {
	snap := protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "demo"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "fix-auth"}},
		Runs: []protocol.Run{
			{ID: "r1", ProjectID: "p1", TaskID: "t1", Agent: "claude", Status: protocol.StatusRunning},
			{ID: "r2", ProjectID: "p1", TaskID: "t1", Agent: "agy", Status: protocol.StatusWaitingInput},
			{ID: "r3", ProjectID: "p1", TaskID: "t1", Agent: "codex", Status: protocol.StatusDone},
		},
	}

	busy, idle := summarizeActiveRunsForStop(snap, 4)
	if len(busy) != 2 {
		t.Fatalf("expected 2 busy runs, got %d (%v)", len(busy), busy)
	}
	if !strings.Contains(busy[0], "demo / fix-auth / claude [运行中]") {
		t.Fatalf("unexpected busy[0]: %q", busy[0])
	}
	if !strings.Contains(busy[1], "demo / fix-auth / agy [等待确认]") {
		t.Fatalf("unexpected busy[1]: %q", busy[1])
	}
	if idle != 2 {
		t.Fatalf("expected 2 other active PTYs, got %d", idle)
	}
}

func TestConfirmStopPromptDefaultsSafeNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"\n", false},
		{"n\n", false},
		{"no\n", false},
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"  YES \n", true},
		{"maybe\n", false},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		got := confirmStopPrompt(strings.NewReader(tc.in), &out)
		if got != tc.want {
			t.Fatalf("confirmStopPrompt(%q) = %v, want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Fatalf("expected [y/N] prompt, got %q", out.String())
		}
	}
}
