package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ICalNode creates an .ics calendar file holding one event per input item
// (n8n's iCalendar "Create Event File").
// Type: "data.ical"
type ICalNode struct{}

func (n *ICalNode) Type() string { return "data.ical" }

// PublishesToThirdParty: data.ical sends nothing off this machine (issue #1418).
func (ICalNode) PublishesToThirdParty() bool { return false }

// icalItemFields are the event fields that usually come from the item
// ({{ $json.title }}), so they are resolved per item, not once per batch.
var icalItemFields = []string{
	"title", "start", "end", "all_day", "attendees", "busy_status", "cal_name",
	"description", "file_name", "geolocation", "location", "recurrence_rule",
	"organizer", "sequence", "status", "uid", "url",
}

func (n *ICalNode) PerItemConfigFields() []string { return icalItemFields }

func (n *ICalNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	items, err := icalItems("data.ical", input, config)
	if err != nil {
		return nil, err
	}
	return mainOutput(items), nil
}

// icalItems builds one .ics file item per input item.
func icalItems(node string, input workflow.NodeInput, config map[string]interface{}) ([]workflow.Item, error) {
	engine := workflow.NewExpressionEngine()
	var out []workflow.Item
	for i, item := range inputItemsOrOne(input.Items) {
		cfg, err := resolveItemConfig(engine, config, icalItemFields, item, input)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", node, i, err)
		}
		ev, calName, err := parseICalConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", node, i, err)
		}
		fileName := cfgString(cfg, "file_name", "event.ics")
		prop := cfgString(cfg, "binary_property_name", defaultBinaryProperty)
		data := buildICS(calName, []icalEvent{ev})
		out = append(out, newFileItem(prop, data, binaryMeta{FileName: fileName, MimeType: "text/calendar", Extension: "ics"}))
	}
	return out, nil
}

// parseICalConfig reads one event from a resolved config.
func parseICalConfig(cfg map[string]interface{}) (icalEvent, string, error) {
	var ev icalEvent
	ev.Title = cfgString(cfg, "title", "")
	ev.AllDay = cfgBool(cfg, "all_day", false)

	start, _, err := parseEventTime(cfg["start"])
	if err != nil {
		return ev, "", fmt.Errorf("start: %w", err)
	}
	end := start
	if v, ok := cfg["end"]; ok && v != nil && fmt.Sprintf("%v", v) != "" {
		if end, _, err = parseEventTime(v); err != nil {
			return ev, "", fmt.Errorf("end: %w", err)
		}
	}
	if end.Before(start) {
		return ev, "", fmt.Errorf("end: %s is before start %s", end.Format("2006-01-02T15:04:05Z07:00"), start.Format("2006-01-02T15:04:05Z07:00"))
	}
	if ev.AllDay {
		// DTEND of an all-day event is exclusive: the day after the last day.
		end = time0(end).AddDate(0, 0, 1)
		start = time0(start)
	}
	ev.Start, ev.End = start, end

	ev.Description = cfgString(cfg, "description", "")
	ev.Location = cfgString(cfg, "location", "")
	ev.URL = cfgString(cfg, "url", "")
	ev.RRule = strings.TrimPrefix(cfgString(cfg, "recurrence_rule", ""), "RRULE:")
	ev.UID = cfgString(cfg, "uid", "")
	if ev.UID == "" {
		ev.UID = uuid.NewString()
	}
	if ev.Status, err = upperChoice(cfg, "status", "CONFIRMED", "CANCELLED", "TENTATIVE"); err != nil {
		return ev, "", err
	}
	if ev.BusyStatus, err = upperChoice(cfg, "busy_status", "BUSY", "TENTATIVE", "FREE", "OOF"); err != nil {
		return ev, "", err
	}
	if v, ok := cfg["sequence"]; ok && v != nil && fmt.Sprintf("%v", v) != "" {
		seq, err := cfgInt(cfg, "sequence", 0)
		if err != nil {
			return ev, "", err
		}
		ev.Sequence = &seq
	}
	if ev.Geo, err = parseGeo(cfg); err != nil {
		return ev, "", err
	}
	org, err := cfgObject(cfg, "organizer")
	if err != nil {
		return ev, "", err
	}
	if org != nil {
		p := icalPerson{Name: str(org["name"]), Email: str(org["email"])}
		if p.Email == "" && p.Name != "" {
			return ev, "", fmt.Errorf("organizer: email is required")
		}
		if p.Email != "" {
			ev.Organizer = &p
		}
	}
	attendees, err := cfgList(cfg, "attendees")
	if err != nil {
		return ev, "", err
	}
	for i, a := range attendees {
		m, ok := a.(map[string]interface{})
		if !ok {
			return ev, "", fmt.Errorf("attendees[%d]: expected an object {name, email, rsvp}, got %T", i, a)
		}
		p := icalPerson{Name: str(m["name"]), Email: str(m["email"]), RSVP: cfgBool(m, "rsvp", false)}
		if p.Email == "" {
			return ev, "", fmt.Errorf("attendees[%d]: email is required", i)
		}
		ev.Attendees = append(ev.Attendees, p)
	}
	return ev, cfgString(cfg, "cal_name", ""), nil
}

func time0(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// upperChoice reads an optional enum value, case-insensitively.
func upperChoice(cfg map[string]interface{}, key string, allowed ...string) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(cfgString(cfg, key, "")))
	if v == "" {
		return "", nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: %q is not one of %s", key, v, strings.Join(allowed, ", "))
}

func parseGeo(cfg map[string]interface{}) (*[2]float64, error) {
	geo, err := cfgObject(cfg, "geolocation")
	if err != nil || geo == nil {
		return nil, err
	}
	if isEmptyValue(geo["lat"]) && isEmptyValue(geo["lon"]) {
		return nil, nil
	}
	lat, ok1 := toFloat(geo["lat"])
	lon, ok2 := toFloat(geo["lon"])
	if !ok1 || !ok2 || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, fmt.Errorf("geolocation: want {lat, lon} in degrees, got %v, %v", geo["lat"], geo["lon"])
	}
	return &[2]float64{lat, lon}, nil
}
