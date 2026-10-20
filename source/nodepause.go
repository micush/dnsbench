package main

import (
	"errors"
	"fmt"
)

// NodePauseState is what the Node page and `dnsdir --node-status` show.
type NodePauseState struct {
	Paused bool `json:"paused"`
}

// NodePauseStatus says whether this node is paused.
func (m *Mgmt) NodePauseStatus() (NodePauseState, error) {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return NodePauseState{}, err
	}
	return NodePauseState{Paused: dc.NodePaused}, nil
}

// NodePause takes this whole node out of service (or puts it back): every gateway
// on it resigns and stops answering and probing, and the other nodes carry on, the
// same as pausing each gateway but in one step and covering gateways added later.
// The flag is local to the node and is never replicated.
func (m *Mgmt) NodePause(pause bool, actor string) (string, error) {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return "", err
	}
	if dc.NodePaused == pause {
		if pause {
			return "this node is already paused", nil
		}
		return "this node is already running", nil
	}
	if pause && m.othersInService() == 0 {
		return "", errors.New("this is the only node in service (the others are paused or not answering), so pausing it would stop every gateway — resume or repair another node first; to stop a gateway everywhere, pause the gateway instead")
	}
	if pause {
		if why := m.lastServerOf(); why != "" {
			return "", errors.New(why)
		}
	}
	dc.NodePaused = pause
	note := "node: resume"
	if pause {
		note = "node: pause"
	}
	if err := m.PutConfig(dc, actor, note); err != nil {
		return "", err
	}
	if pause {
		// the primary role does not stay on a node that is out of service: it goes to the most up-to-date replica
		m.handOver(actor, "pausing this node")
		return "node paused — its gateways stop serving and the other nodes take over; resume it when maintenance is done", nil
	}
	return "node resumed — its gateways rejoin once their DNS servers answer", nil
}

func (m *Mgmt) othersInService() int {
	if m.othersFn != nil {
		return m.othersFn()
	}
	return m.cl.otherNodesInService()
}

// lastServerOf names a gateway this node serves that no other member serves, so pausing the node would leave it with nobody.
// Unlike the update check, a member on another subnet or removed from the gateway is no cover and is not excused: pausing
// the last node of a gateway is refused whatever else the cluster is doing.  "" when it may be paused.
func (m *Mgmt) lastServerOf() string {
	if m.lastFn != nil {
		return m.lastFn()
	}
	if m.gwFn == nil || m.cl == nil || !m.cl.Enabled() {
		return ""
	}
	return lastServing(m.localGateways(), m.cl.peerGateways())
}

func lastServing(mine []GwState, peers []peerGateways) string {
	for _, g := range mine {
		if !g.Serving {
			continue
		}
		covered := false
		for _, p := range peers {
			if p.Down {
				continue
			}
			if !p.Known || p.Serving[g.GroupID] {
				covered = true // an older node cannot say; assume it serves
				break
			}
		}
		if !covered {
			return fmt.Sprintf("this is the only node serving gateway %d, so pausing it would stop that gateway — resume or repair another node that can serve it first; to stop the gateway on purpose, pause the gateway instead", g.GroupID)
		}
	}
	return ""
}
