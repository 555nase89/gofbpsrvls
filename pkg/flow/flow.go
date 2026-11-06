// Package flow runs a goFBP Network in-process and lets callers wait for the
// result of one Run, e.g. an HTTP handler calling the login Network.
package flow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/555nase89/goFBP/pkg/fbp"
)

// ErrNoResult is returned by Call when the Run finished without any Packet
// leaving the Network.
var ErrNoResult = errors.New("flow: run finished without result")

// Runner is a running Instance of a Network. Packets that leave the Network
// in a Run nobody waits for (Send) are logged if they leave through Error.
type Runner struct {
	inst   *fbp.Instance
	logger *slog.Logger

	mu      sync.Mutex
	waiters map[string]chan result // by Run ID
}

type result struct {
	outport string
	p       fbp.Packet
	err     error
}

// Start starts an Instance of net. It stops when ctx is done or on Stop.
func Start(ctx context.Context, net *fbp.Network, logger *slog.Logger) (*Runner, error) {
	r := &Runner{logger: logger, waiters: map[string]chan result{}}
	inst, err := net.Start(ctx, fbp.WithLogger(logger), fbp.WithOutput(r.output), fbp.WithRunFinished(r.finished))
	if err != nil {
		return nil, err
	}
	r.inst = inst
	return r, nil
}

// Stop stops the Instance.
func (r *Runner) Stop() { r.inst.Stop() }

// Send sends v to inport without waiting for the result.
func (r *Runner) Send(ctx context.Context, inport string, v any) error {
	_, err := r.inst.SendAsTo(ctx, inport, v)
	return err
}

// SendData sends data, which must be JSON, to inport without waiting for the
// result.
func (r *Runner) SendData(ctx context.Context, inport string, data []byte) error {
	_, err := r.inst.SendTo(ctx, inport, fbp.Packet{Data: data})
	return err
}

// Call sends v to inport and returns the first Packet of the Run leaving the
// Network, with the name of its Outport. A Packet on fbp.Error is returned as
// is; use ErrorMessage to read it.
func (r *Runner) Call(ctx context.Context, inport string, v any) (string, fbp.Packet, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fbp.Packet{}, err
	}
	runID := fbp.NewRunID()
	ch := make(chan result, 1)
	r.mu.Lock()
	r.waiters[runID] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiters, runID)
		r.mu.Unlock()
	}()
	if _, err := r.inst.SendTo(ctx, inport, fbp.Packet{Data: data, RunID: runID}); err != nil {
		return "", fbp.Packet{}, err
	}
	select {
	case res := <-ch:
		return res.outport, res.p, res.err
	case <-ctx.Done():
		return "", fbp.Packet{}, ctx.Err()
	}
}

// deliver hands res to the waiter of runID, if any, and reports whether
// there was one. Only the first result of a Run is kept.
func (r *Runner) deliver(runID string, res result) bool {
	r.mu.Lock()
	ch, ok := r.waiters[runID]
	r.mu.Unlock()
	if ok {
		select {
		case ch <- res:
		default:
		}
	}
	return ok
}

func (r *Runner) output(_ context.Context, outport string, p fbp.Packet) {
	// Only the message is logged: the input of a Packet may hold secrets,
	// e.g. a password sent to the login Network.
	if !r.deliver(p.RunID, result{outport: outport, p: p}) && outport == fbp.Error {
		e, _ := fbp.DataAs[fbp.ErrorData](p)
		r.logger.Warn("flow: error packet", "run_id", p.RunID, "node", e.Node, "message", e.Message)
	}
}

func (r *Runner) finished(runID string, outputs int) {
	if outputs == 0 {
		r.deliver(runID, result{err: ErrNoResult})
	}
}

// ErrorMessage returns the message of a Packet that left through fbp.Error.
func ErrorMessage(p fbp.Packet) string {
	e, err := fbp.DataAs[fbp.ErrorData](p)
	if err != nil || e.Message == "" {
		return string(p.Data)
	}
	return e.Message
}
