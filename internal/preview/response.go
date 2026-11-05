package preview

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// FetchTimeout bounds untrusted workspace headers and body separately from the
// overall request and its downstream image-write deadline.
const FetchTimeout = 3 * time.Second

// ImageResponse bounds socket writes, including net/http's final buffered flush.
// Its mutex orders deadline shortening against cancellation: a late writer must
// never extend a deadline after revocation has interrupted the connection.
type ImageResponse struct {
	ctx        context.Context
	controller *http.ResponseController
	mu         sync.Mutex
	done       chan struct{}
	stop       func() bool
}

func BoundResponse(ctx context.Context, w http.ResponseWriter) (*ImageResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("image response needs a deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &ImageResponse{ctx: ctx, controller: http.NewResponseController(w), done: make(chan struct{})}
	if err := r.controller.SetWriteDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set image write deadline: %w", err)
	}
	r.stop = context.AfterFunc(ctx, func() {
		r.mu.Lock()
		_ = r.controller.SetWriteDeadline(time.Now())
		r.mu.Unlock()
		close(r.done)
	})
	return r, nil
}

// StartWrite gives normalized images two seconds plus one second per 4 MiB
// (at most four seconds). The request/share deadline may shorten that further.
func (r *ImageResponse) StartWrite(size int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(2*time.Second + time.Duration(min(max(size, 0), MaxScreenBytes))*time.Second/(4<<20))
	if end, _ := r.ctx.Deadline(); end.Before(deadline) {
		deadline = end
	}
	return r.controller.SetWriteDeadline(deadline)
}

// Close joins cancellation before net/http reuses the connection. Leave its
// deadline installed for the final flush; net/http resets it afterward.
func (r *ImageResponse) Close() {
	if !r.stop() {
		<-r.done
	}
}

// ScreenBudget bounds responses and users independently from image processing.
// Fetches and cached writes do not hold the single decoder/encoder slot. Its
// waiters already hold response leases, so both buffers and waiting are bounded.
type ScreenBudget struct {
	mu      sync.Mutex
	active  int
	users   map[string]int
	images  chan struct{}
	limit   int
	perUser int
}

func NewScreenBudget(responses, perUser int) *ScreenBudget {
	return &ScreenBudget{limit: max(1, responses), perUser: max(1, perUser)}
}

// ScreenRequest holds response capacity through the downstream image write.
type ScreenRequest struct {
	budget  *ScreenBudget
	user    string
	image   bool
	waiting bool
	closed  bool
	done    chan struct{}
}

func (b *ScreenBudget) Begin(user string) (*ScreenRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, perUser := b.limit, b.perUser
	if limit == 0 {
		limit, perUser = 4, 1
	}
	if b.active >= limit || b.users[user] >= perUser {
		return nil, false
	}
	if b.users == nil {
		b.users = make(map[string]int)
		b.images = make(chan struct{}, 1)
	}
	b.active++
	b.users[user]++
	return &ScreenRequest{budget: b, user: user, done: make(chan struct{})}, true
}

// BeginImage is called only after the bounded workspace body has arrived. It
// waits for CPU/image capacity, never while waiting for a workspace or a socket.
func (r *ScreenRequest) BeginImage(ctx context.Context) bool {
	b := r.budget
	b.mu.Lock()
	if r.closed || r.image || r.waiting {
		b.mu.Unlock()
		return false
	}
	r.waiting = true
	b.mu.Unlock()
	acquired := false
	select {
	case b.images <- struct{}{}:
		acquired = true
	case <-ctx.Done():
	case <-r.done:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r.waiting = false
	if acquired && (r.closed || ctx.Err() != nil) {
		<-b.images
		return false
	}
	r.image = acquired
	return acquired
}

func (r *ScreenRequest) ImageReady() {
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.image {
		<-b.images
		r.image = false
	}
}

func (r *ScreenRequest) Close() {
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.closed {
		return
	}
	if r.image {
		<-b.images
		r.image = false
	}
	r.closed = true
	close(r.done)
	b.active--
	b.users[r.user]--
	if b.users[r.user] == 0 {
		delete(b.users, r.user)
	}
}
