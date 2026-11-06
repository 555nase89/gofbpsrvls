package record

import (
	"errors"

	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"
)

// LogInport is the extra Inport of the Network record for log records.
const LogInport = "Log"

// Record writes Events (Inport In, EventInput) and log records (Inport Log,
// LogInput) to st. Stored rows leave through Success, invalid input and
// store errors through Error.
func Record(st *store.Store) (*fbp.Network, error) {
	n := &fbp.Network{Name: "record", Description: "writes Events and log records of goFBP Services to the store",
		Inports: []string{LogInport}}
	return n, errors.Join(
		n.Add("checkevent", CheckEvent()),
		n.Add("storeevent", StoreEvent(st)),
		n.Add("checklog", CheckLog()),
		n.Add("storelog", StoreLog(st)),
		n.ConnectIn("checkevent"),
		n.Connect("checkevent", fbp.Success, "storeevent"),
		n.ConnectInport(LogInport, "checklog"),
		n.Connect("checklog", fbp.Success, "storelog"),
		n.ConnectOut("storeevent", fbp.Success, fbp.Success),
		n.ConnectOut("storelog", fbp.Success, fbp.Success),
		n.ConnectOut("checkevent", fbp.Error, fbp.Error),
		n.ConnectOut("storeevent", fbp.Error, fbp.Error),
		n.ConnectOut("checklog", fbp.Error, fbp.Error),
		n.ConnectOut("storelog", fbp.Error, fbp.Error),
	)
}

// Prune deletes the Events and log records older than PruneInput.Before
// and the expired sessions. Each Brick reports a PruneResult on Success.
func Prune(st *store.Store) (*fbp.Network, error) {
	n := &fbp.Network{Name: "prune", Description: "deletes old Events, log records and expired sessions"}
	return n, errors.Join(
		n.Add("pruneevents", PruneEvents(st)),
		n.Add("prunelogs", PruneLogs(st)),
		n.Add("prunesessions", PruneSessions(st)),
		// Fan-out: every Brick gets the Packet.
		n.ConnectIn("pruneevents"),
		n.ConnectIn("prunelogs"),
		n.ConnectIn("prunesessions"),
		n.ConnectOut("pruneevents", fbp.Success, fbp.Success),
		n.ConnectOut("prunelogs", fbp.Success, fbp.Success),
		n.ConnectOut("prunesessions", fbp.Success, fbp.Success),
		n.ConnectOut("pruneevents", fbp.Error, fbp.Error),
		n.ConnectOut("prunelogs", fbp.Error, fbp.Error),
		n.ConnectOut("prunesessions", fbp.Error, fbp.Error),
	)
}
