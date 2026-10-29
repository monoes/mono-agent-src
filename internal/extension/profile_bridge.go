package extension

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/capture"
)

// targetSender is what both the bridge-owning *Server and the relaying
// *RemoteSender offer: commands to a chosen browser, and the question of
// which browser that is.
type targetSender interface {
	commandSender
	SendCommandTo(t Target, cmd *Command, timeout time.Duration) (*Response, error)
	ResolveTarget(t Target) (ConnInfo, error)
	IsConnected() bool
}

var (
	_ targetSender            = (*Server)(nil)
	_ targetSender            = (*RemoteSender)(nil)
	_ browser.ProfileRouter   = (*ServerBridge)(nil)
	_ browser.ProfileRouter   = (*RemoteBridge)(nil)
	_ browser.ExtensionBridge = (*ProfileBridge)(nil)
	_ Capturer                = (*ProfileBridge)(nil)
)

// ForProfile narrows this bridge to the browser bound to profileID.
func (b *ServerBridge) ForProfile(profileID string) browser.ExtensionBridge {
	return &ProfileBridge{via: b.Server, parent: b, profile: profileID}
}

// ForProfile narrows this bridge to the browser bound to profileID.
func (b *RemoteBridge) ForProfile(profileID string) browser.ExtensionBridge {
	return &ProfileBridge{via: b.Sender, parent: b, profile: profileID}
}

// ProfileBridge drives the browser one monoagent profile is bound to.
//
// The first tab it opens pins it to that browser's instance. Everything
// after goes there, even if the user rebinds the browser mid-run, because
// the tabs this bridge holds live in that browser and nowhere else. Create
// one per run (per GetPage), not one per process.
type ProfileBridge struct {
	via     targetSender
	parent  browser.ExtensionBridge
	profile string

	mu       sync.Mutex
	instance string // pinned browser, once a tab exists
	legacy   bool   // the bridge predates routing: send untargeted
}

// Profile is the monoagent profile this bridge runs as.
func (b *ProfileBridge) Profile() string { return b.profile }

// target is where the next command goes.
func (b *ProfileBridge) target() Target {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.legacy:
		return Target{}
	case b.instance != "":
		return Target{Instance: b.instance, Profile: b.profile}
	default:
		return Target{Profile: b.profile}
	}
}

// Route reports the browser this bridge would use now, or why there is
// none (a *NoBrowserError names the fix).
func (b *ProfileBridge) Route() (ConnInfo, error) {
	info, err := b.via.ResolveTarget(b.target())
	if errors.Is(err, ErrBridgeTooOld) {
		b.mu.Lock()
		b.legacy = true
		b.mu.Unlock()
		if b.via.IsConnected() {
			return ConnInfo{Instance: legacyInstanceID, Legacy: true}, nil
		}
		return ConnInfo{}, ErrNoExtension
	}
	return info, err
}

// IsConnected reports whether a browser may run this profile right now.
func (b *ProfileBridge) IsConnected() bool {
	_, err := b.Route()
	return err == nil
}

// pin resolves the browser once and keeps it.
func (b *ProfileBridge) pin() (Target, error) {
	info, err := b.Route()
	if err != nil {
		return Target{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.legacy {
		return Target{}, nil
	}
	if b.instance == "" {
		b.instance = info.Instance
	}
	return Target{Instance: b.instance, Profile: b.profile}, nil
}

func (b *ProfileBridge) CreateTab(url string) (int, error) {
	t, err := b.pin()
	if err != nil {
		return 0, err
	}
	resp, err := b.via.SendCommandTo(t, &Command{
		Type:   CmdCreateTab,
		Params: map[string]interface{}{"url": url},
	}, 30*time.Second)
	if err != nil {
		return 0, err
	}
	return parseTabID(resp)
}

func (b *ProfileBridge) CloseTab(tabID int) error {
	_, err := b.via.SendCommandTo(b.target(), &Command{Type: CmdCloseTab, TabID: tabID}, 30*time.Second)
	return err
}

func (b *ProfileBridge) NewPage(tabID int) browser.PageInterface {
	return NewExtensionPage(pinnedSender{via: b.via, t: b.target()}, tabID)
}

// CapturePage captures in this profile's browser.
func (b *ProfileBridge) CapturePage(req CaptureRequest) (*capture.Result, error) {
	c, ok := b.parent.(Capturer)
	if !ok {
		return nil, fmt.Errorf("this extension bridge cannot capture pages (%T)", b.parent)
	}
	t, err := b.pin()
	if err != nil {
		return nil, err
	}
	req.Target = t
	return c.CapturePage(req)
}

// Addr and PairingURL pass through, so the CLI's connect helpers (port
// hints, auto-pairing) work the same through a narrowed bridge.
func (b *ProfileBridge) Addr() (string, bool) {
	if p, ok := b.parent.(interface{ Addr() (string, bool) }); ok {
		return p.Addr()
	}
	return "", false
}

func (b *ProfileBridge) PairingURL() (string, bool) {
	if p, ok := b.parent.(interface{ PairingURL() (string, bool) }); ok {
		return p.PairingURL()
	}
	return "", false
}

// pinnedSender sends an ExtensionPage's commands to one browser.
type pinnedSender struct {
	via targetSender
	t   Target
}

func (p pinnedSender) SendCommand(cmd *Command, timeout time.Duration) (*Response, error) {
	return p.via.SendCommandTo(p.t, cmd, timeout)
}
