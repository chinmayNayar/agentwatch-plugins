// Package flush is `agentwatch flush`: the detached process that empties the
// spool, refreshes cached passports for active agents and sends the
// heartbeat. One runs at a time (spool lock); it exits when the spool is
// empty, the API is unreachable, or after MaxRun.
package flush

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/install"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
)

var (
	MaxRun            = 2 * time.Minute
	HeartbeatEvery    = time.Minute
	PolicyActiveSince = 10 * time.Minute
)

// Result of one flush.
type Result struct {
	Sent, Dead int
	Heartbeat  bool
	Err        string
}

// Run flushes once. newClient is injectable for tests.
func Run(newClient func(string, string) *client.Client) Result {
	cfg := config.Load()
	cred, err := config.LoadCredential()
	if err != nil {
		return Result{Err: err.Error()}
	}
	if newClient == nil {
		newClient = client.New
	}
	api := newClient(cfg.APIURL, cred.Token)
	sp := spool.New(config.SpoolDir())
	ctx, cancel := context.WithTimeout(context.Background(), MaxRun)
	defer cancel()

	res := Result{}
	for ctx.Err() == nil {
		sent, dead, _ := sp.Flush(ctx, func(r spool.Record) spool.SendResult {
			return send(ctx, api, cfg, r)
		})
		res.Sent += sent
		res.Dead += dead
		if sent == 0 && dead == 0 {
			break
		}
	}
	for _, agent := range state.ActiveAgents(PolicyActiveSince) {
		pctx, pc := context.WithTimeout(ctx, 3*time.Second)
		if p, err := api.Policy(pctx, agent); err == nil {
			state.SavePolicy(p)
		}
		pc()
	}
	if time.Since(state.ReadStamp("heartbeat")) >= HeartbeatEvery {
		if err := Heartbeat(ctx, api, cfg, sp); err == nil {
			res.Heartbeat = true
		}
	}
	return res
}

func send(ctx context.Context, api *client.Client, cfg *config.Config, r spool.Record) spool.SendResult {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	switch r.Type {
	case "ingest":
		var in client.IngestIn
		if json.Unmarshal(r.Body, &in) != nil {
			return spool.Permanent
		}
		if in.EventID == "" {
			in.EventID = r.ID
		}
		err = api.Ingest(rctx, in)
	case "event":
		var ev client.SensorEvent
		if json.Unmarshal(r.Body, &ev) != nil {
			return spool.Permanent
		}
		err = api.Events(rctx, cfg.DeviceName, []client.SensorEvent{ev})
	default:
		return spool.Permanent
	}
	if err == nil {
		return spool.Sent
	}
	if client.IsUnavailable(err) {
		return spool.Retry
	}
	switch client.StatusOf(err) {
	case 401:
		return spool.Retry // signed out: keep until the machine signs in again
	case 409:
		return spool.Permanent // run killed: the server refuses more steps by design
	}
	return spool.Permanent
}

// Heartbeat sends this device's state.
func Heartbeat(ctx context.Context, api *client.Client, cfg *config.Config, sp *spool.Spool) error {
	var cs []client.ClientState
	for _, c := range adapters.Clients {
		st := install.Check(c)
		cs = append(cs, client.ClientState{Client: c, Installed: st.Installed, HooksOK: st.HooksOK, Scope: st.Scope})
	}
	dropped := sp.Dropped()
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := api.Heartbeat(hctx, client.HeartbeatIn{
		DeviceName: cfg.DeviceName, UserLabel: cfg.UserLabel, OS: config.OSArch(), SensorVersion: client.Version,
		Mode: cfg.Mode, FailMode: cfg.FailMode, Clients: cs, SpoolDropped: dropped, SpoolPending: sp.Pending(),
	})
	if err == nil {
		state.WriteStamp("heartbeat")
		if dropped > 0 {
			sp.ResetDropped(dropped)
		}
	}
	return err
}
