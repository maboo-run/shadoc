package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestControllerSkipsDuplicateTaskAndRetriesTransientFailure(t *testing.T) {
	controller := NewController(2)
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan Result, 1)

	go func() {
		firstDone <- controller.Execute(context.Background(), Request{TaskID: "task-a", RepositoryID: "repo-a", MaxAttempts: 1}, func(context.Context) (Status, error) {
			close(started)
			<-release
			return Succeeded, nil
		})
	}()
	<-started

	duplicate := controller.Execute(context.Background(), Request{TaskID: "task-a", RepositoryID: "repo-a", MaxAttempts: 1}, func(context.Context) (Status, error) {
		t.Fatal("duplicate task operation must not run")
		return Failed, nil
	})
	if duplicate.Status != Skipped {
		t.Fatalf("duplicate status = %q", duplicate.Status)
	}
	close(release)
	if result := <-firstDone; result.Status != Succeeded {
		t.Fatalf("first status = %q", result.Status)
	}

	var attempts atomic.Int32
	retried := controller.Execute(context.Background(), Request{TaskID: "task-b", RepositoryID: "repo-b", MaxAttempts: 3, RetryDelay: time.Millisecond}, func(context.Context) (Status, error) {
		if attempts.Add(1) < 3 {
			return Failed, Temporary(errors.New("network unavailable"))
		}
		return Succeeded, nil
	})
	if retried.Status != Succeeded || retried.Attempts != 3 {
		t.Fatalf("retry result = %+v", retried)
	}
}

func TestTerminalStatusNormalizationKeepsOneCanonicalVocabulary(t *testing.T) {
	tests := []struct {
		input string
		want  Status
		ok    bool
	}{
		{input: "success", want: Succeeded, ok: true},
		{input: "succeeded", want: Succeeded, ok: true},
		{input: "partial", want: Partial, ok: true},
		{input: "failed", want: Failed, ok: true},
		{input: "cancelled", want: Cancelled, ok: true},
		{input: "canceled", want: Cancelled, ok: true},
		{input: "skipped", want: Skipped, ok: true},
		{input: "", want: Failed, ok: false},
		{input: "unknown", want: Failed, ok: false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, ok := NormalizeTerminalStatus(test.input)
			if got != test.want || ok != test.ok {
				t.Fatalf("NormalizeTerminalStatus(%q)=(%q,%v), want (%q,%v)", test.input, got, ok, test.want, test.ok)
			}
		})
	}

	for _, value := range []string{"success", "partial", "failed", "cancelled", "skipped"} {
		if _, ok := ParseTerminalStatus(value); !ok {
			t.Fatalf("canonical status %q was rejected", value)
		}
	}
	for _, value := range []string{"succeeded", "canceled", "running", ""} {
		if _, ok := ParseTerminalStatus(value); ok {
			t.Fatalf("non-canonical status %q was accepted", value)
		}
	}
}
