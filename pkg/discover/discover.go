// Package discover finds the goFBP Services connected to NATS. It sends one
// $SRV.STATS request to all micro services and keeps the replies of goFBP
// Services, grouped by Network.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// DefaultWait is how long List collects replies.
const DefaultWait = 500 * time.Millisecond

// Network is one goFBP Network with all its running Services.
type Network struct {
	// Base is the base subject, e.g. "fbp.gofbp.examples.text.pipeline".
	Base     string    `json:"base"`
	Package  string    `json:"package"`
	Name     string    `json:"name"`
	Inports  []string  `json:"inports"`
	Outports []string  `json:"outports"`
	Services []Service `json:"services"`
}

// Service is one running goFBP Service of a Network as reported on $SRV.STATS.
type Service struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Started   time.Time `json:"started"`
	Observing bool      `json:"observing"`
	// Received and Errors are summed over all Inport endpoints.
	Received int `json:"received"`
	Errors   int `json:"errors"`
	Dropped  int `json:"dropped"`
	// Outports counts the Packets published per Network Outport.
	Outports map[string]int `json:"outports"`
}

// List asks all micro services on nc for their statistics and returns the
// goFBP Networks found within wait, sorted by base subject. Services of
// other kinds are ignored. A Service that answers later is missing.
func List(ctx context.Context, nc *nats.Conn, wait time.Duration) ([]Network, error) {
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()
	subject, err := micro.ControlSubject(micro.StatsVerb, "", "")
	if err != nil {
		return nil, err
	}
	if err := nc.PublishRequest(subject, inbox, nil); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	byBase := map[string]*Network{}
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			// Deadline reached: all replies that came in time are collected.
			// No responders: no micro service runs at all.
			if ctx.Err() != nil || errors.Is(err, nats.ErrNoResponders) {
				break
			}
			return nil, err
		}
		var stats micro.Stats
		if json.Unmarshal(msg.Data, &stats) != nil {
			continue
		}
		add(byBase, stats)
	}
	networks := make([]Network, 0, len(byBase))
	for _, n := range byBase {
		slices.SortFunc(n.Services, func(a, b Service) int { return a.Started.Compare(b.Started) })
		networks = append(networks, *n)
	}
	slices.SortFunc(networks, func(a, b Network) int { return strings.Compare(a.Base, b.Base) })
	return networks, nil
}

// add adds the Service described by stats to its Network in byBase, unless
// it is not a goFBP Service.
func add(byBase map[string]*Network, stats micro.Stats) {
	md := stats.Metadata
	base := md["base"]
	if base == "" || md["network"] == "" {
		return
	}
	n := byBase[base]
	if n == nil {
		n = &Network{Base: base, Package: md["package"], Name: md["network"],
			Inports: split(md["inports"]), Outports: split(md["outports"])}
		byBase[base] = n
	}
	svc := Service{ID: stats.ID, Version: stats.Version, Started: stats.Started, Outports: map[string]int{}}
	for _, ep := range stats.Endpoints {
		if ep.Name == service.ObserveEndpoint {
			var obs service.ObserveStats
			if json.Unmarshal(ep.Data, &obs) == nil {
				svc.Observing = obs.Observing
			}
			continue
		}
		svc.Received += ep.NumRequests
		svc.Errors += ep.NumErrors
		var in service.InportStats
		if json.Unmarshal(ep.Data, &in) == nil {
			svc.Dropped += in.Dropped
			for name, c := range in.Outports {
				svc.Outports[name] += c
			}
		}
	}
	n.Services = append(n.Services, svc)
}

func split(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}
