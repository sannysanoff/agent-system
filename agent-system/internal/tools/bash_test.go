package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBashToolDefaultTimeout(t *testing.T) {
	tool := NewBashTool("", 0, nil, nil)

	if got := tool.resolveTimeout(nil); got != 180*time.Second {
		t.Fatalf("expected default timeout 180s, got %v", got)
	}
}

func TestBashToolRequestedTimeout(t *testing.T) {
	tool := NewBashTool("", 180, nil, nil)
	requested := 2

	if got := tool.resolveTimeout(&requested); got != 2*time.Second {
		t.Fatalf("expected requested timeout 2s, got %v", got)
	}
}

func TestBashToolTimeoutCap(t *testing.T) {
	tool := NewBashTool("", 180, nil, nil)
	requested := 901

	if got := tool.resolveTimeout(&requested); got != 900*time.Second {
		t.Fatalf("expected timeout cap 900s, got %v", got)
	}
}

func TestBashToolDefaultTimeoutCap(t *testing.T) {
	tool := NewBashTool("", 901, nil, nil)

	if got := tool.resolveTimeout(nil); got != 900*time.Second {
		t.Fatalf("expected default timeout cap 900s, got %v", got)
	}
}

func TestBashToolTimeoutStopsNestedProcess(t *testing.T) {
	tool := NewBashTool("", 180, nil, nil)
	timeout := 1
	params, err := json.Marshal(BashParams{
		Command: "sh -c 'sleep 5'",
		Timeout: &timeout,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if result.Success {
		t.Fatalf("expected command to time out")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected timeout to return quickly, took %v", elapsed)
	}
	if result.Data.(map[string]interface{})["exit_code"] != 124 {
		t.Fatalf("expected timeout exit code 124, got %v", result.Data.(map[string]interface{})["exit_code"])
	}
}
