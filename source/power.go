package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Power: restart or shut down the host this daemon runs on, now or later.
//
// The scheduling itself is left to shutdown(8) (and so survives a restart of
// ddgw and can be cancelled with shutdown -c).  An immediate action waits a
// moment so the HTTP/CLI reply gets out first.  Unlike the parapet page this
// came from, an immediate action asks the same question an update does — would
// a gateway lose its last serving member? — and refuses unless forced.

// PowerReq is one request.
type PowerReq struct {
	Action  string `json:"action"`  // restart | shutdown | cancel
	When    string `json:"when"`    // now | in | at   (default now)
	Minutes int    `json:"minutes"` // for "in"
	Time    string `json:"time"`    // for "at": 24-hour HH:MM
	Force   bool   `json:"force"`   // go ahead although a gateway would be left unserved
}

// PowerResult is what a request did.
type PowerResult struct {
	Action string `json:"action"`
	When   string `json:"when"`
}

// PowerPending describes a shutdown/reboot that is already scheduled.
type PowerPending struct {
	Scheduled bool   `json:"scheduled"`
	Action    string `json:"action,omitempty"` // restart | shutdown
	At        string `json:"at,omitempty"`     // RFC 3339
}

const maxPowerMinutes = 60 * 24 * 7

var (
	// powerRun runs a command (replaceable in tests).
	powerRun = func(name string, args ...string) error { return exec.Command(name, args...).Run() }
	// powerStart starts one detached (replaceable in tests).
	powerStart = func(name string, args ...string) error { return exec.Command(name, args...).Start() }
	// powerScheduledFile is where systemd records a scheduled shutdown.
	powerScheduledFile = "/run/systemd/shutdown/scheduled"
	// powerDelay is how long an immediate action waits so the reply gets out.
	powerDelay = 800 * time.Millisecond
)

func validHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	return h < 24 && m < 60
}

// PowerStatus reports a pending scheduled power action, if there is one.
func (m *Mgmt) PowerStatus() PowerPending { return readPowerPending(powerScheduledFile) }

func readPowerPending(path string) PowerPending {
	b, err := os.ReadFile(path)
	if err != nil {
		return PowerPending{}
	}
	var usec int64
	mode := ""
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		switch k {
		case "USEC":
			fmt.Sscanf(v, "%d", &usec)
		case "MODE":
			mode = v
		}
	}
	if usec == 0 {
		return PowerPending{}
	}
	act := "shutdown"
	if mode == "reboot" || mode == "kexec" {
		act = "restart"
	}
	return PowerPending{Scheduled: true, Action: act, At: time.UnixMicro(usec).UTC().Format(time.RFC3339)}
}

// Power carries out a request.  by is the actor, for the log.
func (m *Mgmt) Power(r PowerReq, by string) (PowerResult, error) {
	if r.Action == "cancel" {
		if err := powerRun("shutdown", "-c"); err != nil {
			return PowerResult{}, errors.New("nothing to cancel (no power action is scheduled)")
		}
		warnf("power: scheduled power action cancelled by %s", by)
		return PowerResult{Action: "cancel"}, nil
	}
	var flag, sysctl, verb string
	switch r.Action {
	case "restart", "reboot":
		flag, sysctl, verb, r.Action = "-r", "reboot", "restart", "restart"
	case "shutdown", "poweroff":
		flag, sysctl, verb, r.Action = "-h", "poweroff", "shut down", "shutdown"
	default:
		return PowerResult{}, errors.New("action must be restart, shutdown or cancel")
	}
	spec, human := "now", "now"
	switch r.When {
	case "", "now":
		// An immediate action must not take the last serving member of a gateway down.
		if ok, why := m.updateSafe(); !ok && !r.Force {
			return PowerResult{}, fmt.Errorf("not safe to %s this node now: %s (to go ahead anyway: ddgw --power %s --yes, or confirm in the GUI)", verb, why, r.Action)
		}
	case "in":
		if r.Minutes < 1 || r.Minutes > maxPowerMinutes {
			return PowerResult{}, fmt.Errorf("minutes must be between 1 and %d (7 days)", maxPowerMinutes)
		}
		spec, human = fmt.Sprintf("+%d", r.Minutes), fmt.Sprintf("in %d minute(s)", r.Minutes)
	case "at":
		if !validHHMM(r.Time) {
			return PowerResult{}, errors.New("time must be 24-hour HH:MM")
		}
		spec, human = r.Time, "at "+r.Time
	default:
		return PowerResult{}, errors.New("when must be now, in or at")
	}
	warnf("power: host %s %s requested by %s", verb, human, by)
	go func() {
		time.Sleep(powerDelay)
		if spec == "now" && powerStart("systemctl", sysctl) == nil {
			return
		}
		if err := powerStart("shutdown", flag, spec); err != nil {
			errorf("power: could not run shutdown: %v", err)
		}
	}()
	return PowerResult{Action: r.Action, When: human}, nil
}
