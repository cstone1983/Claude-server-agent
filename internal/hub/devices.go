package hub

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// Remembered devices: after one login (password + authenticator code), or
// one approval with a code, a browser can be remembered. A remembered browser
// is logged in to the dashboard and approves without a code, except for
// requests that need their own OK every time (e.g. rebooting a Proxmox host).
//
// The cookie is long-lived and renewed on every use, so a device in regular
// use stays remembered. The hub stores only a hash of the cookie; forgetting a
// device (dashboard, logout on that browser, or `csa-hub` session revocation)
// ends it immediately.

const (
	deviceCookie = "csa_device"
	// deviceMaxAge is the cookie lifetime, renewed on each use. Browsers cap
	// cookie lifetimes at about 400 days.
	deviceMaxAge = 400 * 24 * time.Hour
	// deviceTouchEvery limits how often "last used" is written to the state file.
	deviceTouchEvery = time.Hour
)

// TrustedDevice is a remembered browser.
type TrustedDevice struct {
	ID    string `json:"id"`    // public id, for listing and forgetting
	Label string `json:"label"` // detected, e.g. "Chrome on Windows"
	// Name is the user's own name for the device ("Chris's phone"); empty
	// means use Label.
	Name      string    `json:"name,omitempty"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
}

// rememberDevice creates a remembered device for this browser and sets its cookie.
func (h *Hub) rememberDevice(w http.ResponseWriter, r *http.Request, via string) {
	tok := randomToken("", 32)
	dev := &TrustedDevice{ID: randomToken("dev_", 6), Label: deviceLabel(r.UserAgent()), IP: clientIP(r),
		CreatedAt: time.Now(), LastUsed: time.Now()}
	h.store.Update(func(d *stateData) error {
		pruneDevices(d)
		d.Devices[hashSecret(tok)] = dev
		return nil
	})
	h.setDeviceCookie(w, tok)
	h.audit.Write(audit.Entry{Event: "device_remembered", Detail: map[string]string{"id": dev.ID, "label": dev.Label, "ip": dev.IP, "via": via}})
}

func (h *Hub) setDeviceCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: tok, Path: "/", MaxAge: int(deviceMaxAge.Seconds()),
		HttpOnly: true, Secure: strings.HasPrefix(h.cfg.PublicURL, "https://"),
		// Lax so the cookie comes along when an approval link is opened from
		// another site (the Claude app). Cross-site POSTs are refused by the
		// CSRF check on every form.
		SameSite: http.SameSiteLaxMode,
	})
}

// trustedDevice returns the remembered device for this request, if any.
func (h *Hub) trustedDevice(r *http.Request) (TrustedDevice, bool) {
	c, err := r.Cookie(deviceCookie)
	if err != nil || c.Value == "" {
		return TrustedDevice{}, false
	}
	var dev TrustedDevice
	found := false
	h.store.View(func(d *stateData) {
		if x := d.Devices[hashSecret(c.Value)]; x != nil {
			dev, found = *x, true
		}
	})
	return dev, found
}

// useDevice marks this request's remembered device as used and renews its
// cookie. It returns false if the browser is not remembered.
func (h *Hub) useDevice(w http.ResponseWriter, r *http.Request) bool {
	dev, ok := h.trustedDevice(r)
	if !ok {
		return false
	}
	c, _ := r.Cookie(deviceCookie)
	if time.Since(dev.LastUsed) > deviceTouchEvery {
		key := hashSecret(c.Value)
		h.store.Update(func(d *stateData) error {
			if x := d.Devices[key]; x != nil {
				x.LastUsed, x.IP = time.Now(), clientIP(r)
			}
			return nil
		})
	}
	h.setDeviceCookie(w, c.Value)
	return true
}

// DisplayName is the user's name for the device, or the detected label.
func (d TrustedDevice) DisplayName() string {
	if d.Name != "" {
		return d.Name
	}
	return d.Label
}

// maxDeviceName limits device names (they are shown on the dashboard).
const maxDeviceName = 60

// renameDevice sets a remembered device's name; an empty name goes back to
// the detected label.
func (h *Hub) renameDevice(id, name, via string) (bool, error) {
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > maxDeviceName {
		return false, errors.New("the name is too long (60 characters at most)")
	}
	found := false
	h.store.Update(func(d *stateData) error {
		for _, x := range d.Devices {
			if x.ID == id {
				x.Name = name
				found = true
			}
		}
		return nil
	})
	if found {
		h.audit.Write(audit.Entry{Event: "device_renamed", Detail: map[string]string{"id": id, "name": name, "via": via}})
	}
	return found, nil
}

// forgetDevice removes a remembered device by its public id.
func (h *Hub) forgetDevice(id, via string) bool {
	found := false
	h.store.Update(func(d *stateData) error {
		for k, x := range d.Devices {
			if x.ID == id {
				delete(d.Devices, k)
				found = true
			}
		}
		return nil
	})
	if found {
		h.audit.Write(audit.Entry{Event: "device_forgotten", Detail: map[string]string{"id": id, "via": via}})
	}
	return found
}

// forgetThisDevice forgets the browser making the request and clears its cookie.
func (h *Hub) forgetThisDevice(w http.ResponseWriter, r *http.Request, via string) {
	if dev, ok := h.trustedDevice(r); ok {
		h.forgetDevice(dev.ID, via)
	}
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: "", Path: "/", MaxAge: -1})
}

// devices lists remembered devices, most recently used first.
func (h *Hub) devices() []TrustedDevice {
	var out []TrustedDevice
	h.store.View(func(d *stateData) {
		for _, x := range d.Devices {
			out = append(out, *x)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out
}

// pruneDevices drops devices whose cookie has certainly expired (unused for
// longer than the cookie lifetime).
func pruneDevices(d *stateData) {
	for k, x := range d.Devices {
		if time.Since(x.LastUsed) > deviceMaxAge {
			delete(d.Devices, k)
		}
	}
}

// deviceLabel turns a User-Agent into a short "Browser on OS" label.
func deviceLabel(ua string) string {
	os := "unknown OS"
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		os = "Mac"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "SamsungBrowser"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	return browser + " on " + os
}
