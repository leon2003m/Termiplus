package preview

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu      sync.Mutex
	calls   int
	started chan struct{}
	finish  chan struct{}
}

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error {
	w.mu.Lock()
	w.calls++
	call := w.calls
	w.mu.Unlock()
	if call == 2 {
		close(w.started)
		<-w.finish
	}
	return nil
}

func TestBoundResponseJoinsCancellationBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), finish: make(chan struct{})}
	cleanup, err := BoundResponse(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the write deadline")
	}
	done := make(chan struct{})
	go func() { cleanup.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("cleanup allowed a deadline callback to outlive the handler")
	case <-time.After(20 * time.Millisecond):
	}
	close(w.finish)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not join the deadline callback")
	}
}

func TestBoundResponseRequiresDeadlineSupport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := BoundResponse(ctx, httptest.NewRecorder()); err == nil {
		t.Fatal("accepted a writer without socket deadline support")
	}
	if _, err := BoundResponse(context.Background(), httptest.NewRecorder()); err == nil {
		t.Fatal("accepted an unbounded response context")
	}
}

func TestScreenBudgetBoundsViewersAndImageMemory(t *testing.T) {
	var budget ScreenBudget
	first, ok := budget.Begin("viewer-a")
	if !ok || !first.BeginImage(context.Background()) {
		t.Fatal("first image refused")
	}
	defer first.Close()
	if work, ok := budget.Begin("viewer-a"); ok {
		work.Close()
		t.Fatal("duplicate viewer admitted")
	}
	second, ok := budget.Begin("viewer-b")
	if !ok {
		t.Fatal("second viewer refused")
	}
	defer second.Close()
	blocked := func(r *ScreenRequest) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if r.BeginImage(ctx) {
			t.Fatal("allowed two image allocations at once")
		}
	}
	blocked(second)
	first.ImageReady() // its already-normalized image can still be writing
	if !second.BeginImage(context.Background()) {
		t.Fatal("blocked writer retained the decoder")
	}
	first.Close()
	first.Close()
	third, ok := budget.Begin("viewer-c")
	if !ok {
		t.Fatal("completed response retained capacity")
	}
	defer third.Close()
	blocked(third) // closing an old response must not free another image slot
	second.Close()
	if !third.BeginImage(context.Background()) {
		t.Fatal("failed response retained image capacity")
	}
	fourth, ok := budget.Begin("viewer-d")
	if !ok {
		t.Fatal("fourth viewer refused")
	}
	defer fourth.Close()
	waiting := make(chan bool, 1)
	go func() { waiting <- fourth.BeginImage(context.Background()) }()
	fourth.Close()
	select {
	case acquired := <-waiting:
		if acquired {
			t.Fatal("closed request acquired an image slot")
		}
	case <-time.After(time.Second):
		t.Fatal("closed request remained queued")
	}
}

func TestScreenBudgetCountsGatewayUsers(t *testing.T) {
	budget := NewScreenBudget(8, 4)
	var leases []*ScreenRequest
	defer func() {
		for _, r := range leases {
			r.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		r, ok := budget.Begin("owner-a")
		if !ok {
			t.Fatal("concurrent owner requests were made exclusive")
		}
		leases = append(leases, r)
	}
	if r, ok := budget.Begin("owner-a"); ok {
		r.Close()
		t.Fatal("per-user bound exceeded")
	}
	for i := 0; i < 4; i++ {
		r, ok := budget.Begin("owner-b")
		if !ok {
			t.Fatal("one user consumed another user's capacity")
		}
		leases = append(leases, r)
	}
	if r, ok := budget.Begin("owner-c"); ok {
		r.Close()
		t.Fatal("global bound exceeded")
	}
	leases[0].Close()
	if r, ok := budget.Begin("owner-a"); !ok {
		t.Fatal("per-user count did not decrease")
	} else {
		leases = append(leases, r)
	}
}

type writeDeadlineRecorder struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	deadline time.Time
}

func (w *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = deadline
	return nil
}

func TestImageWriteDeadlineIsShortAndCannotUndoCancellation(t *testing.T) {
	for _, size := range []int{1, MaxScreenBytes} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		w := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		r, err := BoundResponse(ctx, w)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := r.StartWrite(size); err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		deadline := w.deadline
		w.mu.Unlock()
		if deadline.Before(start.Add(2*time.Second)) || deadline.After(time.Now().Add(4*time.Second)) {
			t.Fatalf("unbounded write deadline: %v", deadline)
		}
		cancel()
		if err := r.StartWrite(size); err == nil {
			t.Fatal("cancelled response extended its deadline")
		}
		r.Close()
		w.mu.Lock()
		deadline = w.deadline
		w.mu.Unlock()
		if deadline.After(time.Now()) {
			t.Fatal("cancellation deadline was overwritten")
		}
	}
}
