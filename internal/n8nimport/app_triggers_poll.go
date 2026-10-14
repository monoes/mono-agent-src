package n8nimport

import (
	"encoding/json"
	"fmt"
	"strings"
)

// pollTriggers lists n8n's polling app triggers by node name (without the
// "n8n-nodes-base." prefix). Each plan picks the app package's list
// operation and the value identifying a new record; see map_app_trigger.go.
// Time windows use $now in the list step's params, so each run asks only
// for the last few poll intervals.
var pollTriggers = map[string]pollSpec{
	"gmailTrigger":                      {Plan: planGmail},
	"googleSheetsTrigger":               {Plan: planSheets},
	"airtableTrigger":                   {Plan: planAirtable},
	"googleCalendarTrigger":             {Plan: planCalendar},
	"googleDriveTrigger":                {Plan: planDrive},
	"notionTrigger":                     {Plan: planNotion},
	"microsoftOutlookTrigger":           {Plan: planOutlook},
	"salesforceTrigger":                 {Plan: planSalesforce},
	"microsoftSharePointTrigger":        {Plan: planSharePoint},
	"microsoftOneDriveTrigger":          {Plan: planOneDrive},
	"googleBusinessProfileTrigger":      {Plan: planBusinessProfile},
	"seaTableTrigger":                   {Plan: planSeaTable},
	"clockifyTrigger":                   {Plan: planClockify},
	"togglTrigger":                      {Plan: planToggl},
	"databricksTrigger":                 {Plan: planDatabricks},
	"venafiTlsProtectDatacenterTrigger": {Plan: planVenafiDatacenter},
}

const (
	rfc3339Z    = "2006-01-02T15:04:05Z"
	nowRFC3339Z = "{{ $now.UTC.Format `2006-01-02T15:04:05Z` }}"
)

// rlValue reads an n8n resource locator parameter as its plain value (an
// expression is translated).
func (c *MapContext) rlValue(path string) interface{} {
	if m, ok := c.Raw(path).(map[string]interface{}); ok {
		return c.Translate(path, m["value"])
	}
	return c.Value(path)
}

func planGmail(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "message", "getAll"
	c.Literal("event", "messageReceived")
	p.copyParams(c, map[string]string{"simple": "simple", "options": "options"})
	filters, _ := c.Value("filters").(map[string]interface{})
	if filters == nil {
		filters = map[string]interface{}{}
	}
	if v, ok := filters["includeDrafts"]; ok && v != false {
		c.Warn("filters.includeDrafts", v, "drafts are not listed")
	}
	filters["receivedAfter"] = sinceExpr(p.Lookback, rfc3339Z)
	p.Config["filters"] = filters
	p.Dedupe = "={{ $json.id }}"
	return ""
}

func planSheets(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "sheet", "read"
	p.copyParams(c, map[string]string{"documentId": "documentId", "sheetName": "sheetName"})
	opts := map[string]interface{}{}
	if c.Has("options.dataLocationOnSheet") {
		opts["dataLocationOnSheet"] = c.Value("options.dataLocationOnSheet")
	}
	format := map[string]interface{}{}
	if c.Has("options.valueRender") {
		format["general"] = c.Value("options.valueRender")
	}
	if c.Has("options.dateTimeRenderOption") {
		format["date"] = c.Value("options.dateTimeRenderOption")
	}
	if len(format) > 0 {
		opts["outputFormatting"] = map[string]interface{}{"values": format}
	}
	if len(opts) > 0 {
		p.Config["options"] = opts
	}
	switch ev := c.Literal("event", "anyUpdate"); ev {
	case "rowAdded":
		c.Warn("event", ev, "rows are compared by content: an edited row passes again, like a new one")
	case "rowUpdate":
		c.Warn("event", ev, "rows are compared by content: new rows pass too, not only edited ones")
	}
	if inc := c.Literal("includeInOutput", "new"); inc != "new" {
		c.Warn("includeInOutput", inc, "only the current row is output; the previous version is not known")
	}
	if c.Has("options.columnsToWatch") {
		c.Unsupported("options.columnsToWatch")
	}
	return ""
}

func planAirtable(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "record", "search"
	p.Config["base"] = c.rlValue("baseId")
	p.Config["table"] = c.rlValue("tableId")
	field := c.Literal("triggerField", "")
	if field == "" {
		return "triggerField (the created/last-modified time field) is not set"
	}
	formula := fmt.Sprintf(`IS_AFTER({%s}, DATETIME_PARSE("%s"))`, field, sinceExpr(p.Lookback, rfc3339Z))
	if user := c.Str("additionalFields.formula", ""); user != "" {
		formula = fmt.Sprintf("AND(%s, %s)", formula, user)
	}
	p.Config["filterByFormula"] = formula
	p.Config["returnAll"] = true
	opts := map[string]interface{}{}
	if f := c.Literal("additionalFields.fields", ""); f != "" {
		fields := commaList(f)
		if !contains(fields, field) {
			fields = append(fields, field)
		}
		opts["fields"] = fields
	}
	if c.Has("additionalFields.viewId") {
		opts["view"] = c.Value("additionalFields.viewId")
	}
	if len(opts) > 0 {
		p.Config["options"] = opts
	}
	if c.Bool("downloadAttachments", false) {
		c.Warn("downloadAttachments", true, "attachments are not downloaded")
	}
	c.Ignore("downloadFieldNames")
	key, _ := json.Marshal(field)
	p.Dedupe = fmt.Sprintf(`={{ $json.id }}|{{ $json.fields[%s] }}`, key)
	return ""
}

func planCalendar(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "event", "getAll"
	p.Config["calendar"] = c.Value("calendarId")
	p.Config["returnAll"] = true
	opts := map[string]interface{}{}
	if c.Has("options.matchTerm") {
		opts["query"] = c.Value("options.matchTerm")
	}
	since := sinceExpr(p.Lookback, rfc3339Z)
	p.Dedupe = "={{ $json.id }}"
	switch ev := c.Literal("triggerOn", ""); ev {
	case "eventCreated":
		opts["updatedMin"] = since
	case "eventUpdated":
		opts["updatedMin"] = since
		p.Dedupe = "={{ $json.id }}|{{ $json.updated }}"
	case "eventCancelled":
		opts["updatedMin"] = since
		opts["showDeleted"] = true
		p.Dedupe = "={{ $json.id }}|{{ $json.status }}"
		c.Warn("triggerOn", ev, "every changed event passes, not only cancelled ones: check status = cancelled after this node")
	case "eventStarted":
		p.Config["timeMin"] = since
		p.Config["timeMax"] = nowRFC3339Z
	case "eventEnded":
		return "an event's end cannot be selected by the Calendar list filters"
	default:
		return fmt.Sprintf("triggerOn %q is not known", ev)
	}
	p.Config["options"] = opts
	return ""
}

func planDrive(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "fileFolder", "search"
	filter := map[string]interface{}{}
	on := c.Literal("triggerOn", "")
	if on == "specificFile" || (on == "specificFolder" && c.Literal("event", "") == "watchFolderUpdated") {
		// One file (or the watched folder itself): read it each poll and
		// pass it on when its modifiedTime changed.
		p.Resource, p.Operation = "file", "get"
		if on == "specificFile" {
			c.Literal("event", "fileUpdated")
			p.Config["fileId"] = c.rlValue("fileToWatch")
		} else {
			p.Config["fileId"] = c.rlValue("folderToWatch")
		}
		p.Dedupe = "={{ $json.id }}|{{ $json.modifiedTime }}"
		return ""
	}
	switch on {
	case "specificFolder":
		filter["folderId"] = c.rlValue("folderToWatch")
	case "anyFileFolder":
		if c.Has("driveToWatch") {
			filter["driveId"] = c.rlValue("driveToWatch")
		}
	default:
		return fmt.Sprintf("triggerOn %q is not known", on)
	}
	stamp, mime := "createdTime", " and mimeType != 'application/vnd.google-apps.folder'"
	switch ev := c.Literal("event", ""); ev {
	case "fileCreated":
	case "fileUpdated":
		stamp = "modifiedTime"
	case "folderCreated":
		mime = " and mimeType = 'application/vnd.google-apps.folder'"
	case "folderUpdated":
		stamp, mime = "modifiedTime", " and mimeType = 'application/vnd.google-apps.folder'"
	default:
		return fmt.Sprintf("event %q (a change to the watched folder itself) has no Drive search equivalent", ev)
	}
	if t := c.Literal("options.fileType", "all"); t != "all" {
		mime = fmt.Sprintf(" and mimeType = '%s'", t)
	}
	p.Config["searchMethod"] = "query"
	p.Config["queryString"] = fmt.Sprintf("%s > '%s'%s", stamp, sinceExpr(p.Lookback, rfc3339Z), mime)
	p.Config["filter"] = filter
	p.Config["returnAll"] = true
	p.Config["options"] = map[string]interface{}{"fields": []interface{}{"id", "name", "mimeType", "createdTime", "modifiedTime", "webViewLink", "parents"}}
	p.Dedupe = "={{ $json.id }}"
	if stamp == "modifiedTime" {
		p.Dedupe = "={{ $json.id }}|{{ $json.modifiedTime }}"
	}
	return ""
}

func planNotion(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "databasePage", "getAll"
	p.copyParams(c, map[string]string{"simple": "simple"})
	if c.Has("dataSourceId") {
		p.Config["dataSourceId"] = c.Value("dataSourceId")
	}
	if c.Has("databaseId") {
		p.Config["databaseId"] = c.Value("databaseId")
	}
	stamp := "created_time"
	p.Dedupe = "={{ $json.id }}"
	if ev := c.Literal("event", "pageAddedToDatabase"); ev == "pagedUpdatedInDatabase" {
		stamp = "last_edited_time"
		p.Dedupe = ""
	}
	p.Config["returnAll"] = true
	p.Config["filterType"] = "json"
	p.Config["filterJson"] = fmt.Sprintf(`{"timestamp": %q, %q: {"on_or_after": %q}}`, stamp, stamp, sinceExpr(p.Lookback, rfc3339Z))
	return ""
}

func planOutlook(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "message", "getAll"
	c.Literal("event", "messageReceived")
	p.copyParams(c, map[string]string{"output": "output", "fields": "fields", "options": "options"})
	filters, _ := c.Value("filters").(map[string]interface{})
	if filters == nil {
		filters = map[string]interface{}{}
	}
	filters["receivedAfter"] = sinceExpr(p.Lookback, rfc3339Z)
	p.Config["filtersUI"] = map[string]interface{}{"values": map[string]interface{}{"filterBy": "filters", "filters": filters}}
	p.Config["returnAll"] = true
	p.Dedupe = "={{ $json.id }}"
	return ""
}

func planSalesforce(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "search", "query"
	on := c.Literal("triggerOn", "")
	kind, stamp := strings.TrimSuffix(on, "Created"), "CreatedDate"
	if strings.HasSuffix(on, "Updated") {
		kind, stamp = strings.TrimSuffix(on, "Updated"), "LastModifiedDate"
	}
	object := salesforceObjects[kind]
	if kind == "customObject" {
		object = c.Literal("customObject", "")
	}
	if object == "" {
		return fmt.Sprintf("triggerOn %q names no Salesforce object", on)
	}
	p.Config["query"] = fmt.Sprintf("SELECT FIELDS(ALL) FROM %s WHERE %s >= %s ORDER BY %s DESC LIMIT 200",
		object, stamp, sinceExpr(p.Lookback, rfc3339Z), stamp)
	p.Dedupe = "={{ $json.Id }}"
	if stamp == "LastModifiedDate" {
		p.Dedupe = "={{ $json.Id }}|{{ $json.LastModifiedDate }}"
	}
	return ""
}

func planSharePoint(c *MapContext, p *pollPlan) string {
	library := c.Literal("resource", "documentLibrary") != "list"
	what := "list items"
	if library {
		what = "files"
	}
	// A poll sees the current items only, so deletions (in n8n's default
	// events) are lost; changes are what it can report.
	events := fmt.Sprint(c.Raw("events"))
	if c.Has("events") && !strings.Contains(events, "changed") {
		return "deleted " + what + " cannot be listed"
	}
	if !c.Has("events") || strings.Contains(events, "deleted") {
		c.Warn("events", c.Raw("events"), "deleted %s are not reported: the poll lists the current %s and passes on new or changed ones", what, what)
	}
	p.Config["site"] = c.Value("site")
	p.Dedupe = "={{ $json.id }}|{{ $json.lastModifiedDateTime }}"
	if library {
		p.Resource, p.Operation = "file", "getChanges"
		if c.Has("drive") {
			p.Config["drive"] = c.Value("drive")
		}
		return ""
	}
	p.Resource, p.Operation = "item", "getAll"
	p.Config["list"] = c.Value("list")
	p.Config["returnAll"] = true
	return ""
}

func planOneDrive(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "folder", "getChildren"
	if c.Bool("watchFolder", false) {
		p.Config["folderId"] = c.rlValue("folderId")
	} else {
		p.Config["folderId"] = "root"
		c.Warn("watchFolder", false, "only the drive's top folder is listed, not every folder below it")
	}
	c.Ignore("watch", "options.folderChild", "fileId")
	p.Dedupe = "={{ $json.id }}"
	if ev := c.Literal("event", "fileCreated"); ev == "fileUpdated" || ev == "folderUpdated" {
		p.Dedupe = "={{ $json.id }}|{{ $json.lastModifiedDateTime }}"
	}
	c.Note("files and folders are listed together; check each item's file or folder key if you need one kind")
	c.Ignore("simple")
	return ""
}

func planBusinessProfile(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "review", "getAll"
	c.Literal("event", "reviewAdded")
	p.Config["account"] = c.Value("account")
	p.Config["location"] = c.Value("location")
	p.Dedupe = "={{ $json.name }}"
	return ""
}

func planSeaTable(c *MapContext, p *pollPlan) string {
	p.Resource, p.Operation = "row", "list"
	p.Config["tableName"] = c.Value("tableName")
	p.copyParams(c, map[string]string{"viewName": "viewName"})
	p.Config["options"] = map[string]interface{}{"simple": false}
	switch ev := c.Literal("event", "newRow"); ev {
	case "newRow":
		p.Dedupe = "={{ $json._id }}"
	case "updatedRow":
		p.Dedupe = "={{ $json._id }}|{{ $json._mtime }}"
	default:
		return "new assets cannot be listed"
	}
	c.Ignore("options", "assetColumn")
	return ""
}
