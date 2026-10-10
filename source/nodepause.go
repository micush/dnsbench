package main

// NodePauseState is what the Node page and `ddgw --node-status` show.
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
	dc.NodePaused = pause
	note := "node: resume"
	if pause {
		note = "node: pause"
	}
	if err := m.PutConfig(dc, actor, note); err != nil {
		return "", err
	}
	if pause {
		return "node paused — its gateways stop serving and the other nodes take over; resume it when maintenance is done", nil
	}
	return "node resumed — its gateways rejoin once their DNS servers answer", nil
}
