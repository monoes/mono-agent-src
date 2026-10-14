package catalog

// Reference entries for the file conversion and file system nodes
// (internal/nodes/data: convert_to_file, extract_from_file, read_write_file,
// ical). catalog.go appends them to NodeDocs.

const binaryNote = `Files travel on items as a base64 field (the "binary property", default
"data") with file_name, mime_type, file_extension and file_size next to it;
several files on one item are listed in "files".`

var dataFilesNodeDocs = []nodeDoc{
	{
		Type:     "data.convert_to_file",
		Category: "data",
		Short:    "Turn items into a file: CSV, XLSX, ODS, HTML, RTF, JSON, text, ICS or base64",
		Description: `csv, html, ods, rtf and xlsx write all input items to one table (nested
fields flattened to "a.b" columns, columns sorted). json writes all items as
one array (mode once) or one file per item (mode each). text writes one
field's text per item, to_binary decodes one field's base64 string per item,
ics builds one calendar event per item (same fields as data.ical).`,
		Config: `{
  "operation": "xlsx",              // csv | html | ics | json | ods | rtf | text | xlsx | to_binary
  "binary_property_name": "data",   // where the file goes
  "file_name": "report.xlsx",       // default File.<ext>, file.json, file.txt, event.ics
  "header_row": true,               // tables: write column names first
  "sheet_name": "Sheet",            // ods / xlsx
  "compression": false,             // ods / xlsx: deflate the parts
  "delimiter": ",",                 // csv
  "mode": "once",                   // json: once | each
  "format": false,                  // json: pretty-print
  "source_property": "content",     // text / to_binary: field to read
  "encoding": "utf8",               // json / text / to_binary(data_is_base64=false)
  "add_bom": false,
  "mime_type": ""                   // to_binary / text
}`,
		Inputs:  "items (tables, json once) or one item per file (text, to_binary, ics, json each)",
		Outputs: "items carrying the file as a binary property",
		Notes:   binaryNote + ` xls (Excel 97-2003) is not supported; use xlsx.`,
	},
	{
		Type:     "data.extract_from_file",
		Category: "data",
		Short:    "Read a file on the item: CSV, XLSX, ODS, HTML, RTF, PDF, JSON, XML, ICS, text",
		Description: `csv, html (first <table>), ods, rtf (its table) and xlsx return one item per
row, keyed by the header row (or {"row": [...]} without one). json, text,
xml (as a string), ics (parsed events) and binary_to_property (base64) put the
content in destination_key. pdf returns {text, numpages, numrender, info}.`,
		Config: `{
  "operation": "csv",               // csv | html | ics | json | ods | pdf | rtf | text | xml | xlsx | binary_to_property
  "binary_property_name": "data",
  "header_row": true, "include_empty_cells": false, "max_rows": -1,
  "delimiter": ",", "from_line": 0, "relax_quotes": false,   // csv
  "sheet_name": "", "range": "A1:D100", "raw_data": false,   // ods / xlsx
  "destination_key": "data",        // json / text / xml / ics / binary_to_property
  "keep_source": "json",            // json | binary | both (also pdf)
  "encoding": "utf8", "strip_bom": true,
  "join_pages": true, "max_pages": 0, "password": ""         // pdf
}`,
		Inputs:  "items carrying a file (from read_write_file, http.request, email, …)",
		Outputs: "one item per row (tables) or one item per file",
		Notes: binaryNote + ` xls (Excel 97-2003) is not supported; save as xlsx.
RTF without a table returns {"text": ...}. PDF text extraction is pure Go and
best-effort (scanned PDFs have no text to extract).`,
	},
	{
		Type:     "data.read_write_file",
		Category: "data",
		Short:    "Read files from disk (glob patterns) or write an item's file to disk",
		Description: `read: every regular file matching file_selector becomes one item (patterns:
* ? within a name, ** across directories, {a,b} alternatives; dot files only
when the pattern names them). write: writes the item's binary property to
file_name, creating parent directories, replacing or appending.`,
		Config: `{
  "operation": "read",                 // read | write
  "file_selector": "reports/**/*.csv", // read
  "file_name": "out/report.csv",       // write: path; read: rename the files read
  "data_property_name": "data",
  "append": false,                     // write
  "mime_type": "", "file_extension": "", "literal_brackets": true   // read
}`,
		Inputs:  "any items (each item runs the operation once; per-item expressions allowed)",
		Outputs: "read: one item per file (with file_path); write: the input item plus file_path, bytes_written",
		Notes: binaryNote + ` In a run started by an org role, every path (the
pattern's directory, each match, the write target) must stay inside the org
workdir; relative paths start there. Files over 256 MiB are refused.`,
	},
	{
		Type:     "data.ical",
		Category: "data",
		Short:    "Create an .ics calendar event file per item",
		Description: `Builds an RFC 5545 calendar file with one VEVENT per input item. Every field
is resolved per item, so {{ $json.start }} etc. make one event per item.
Timed events are written in UTC; all-day events as dates (end inclusive).`,
		Config: `{
  "title": "{{ $json.title }}",
  "start": "2026-10-13T09:30:00Z",   // RFC 3339, a date, or a Unix epoch
  "end":   "2026-10-13T10:30:00Z",   // default: start
  "all_day": false,
  "binary_property_name": "data",
  "file_name": "event.ics",
  "description": "", "location": "", "url": "", "uid": "",
  "status": "CONFIRMED",             // CONFIRMED | CANCELLED | TENTATIVE
  "busy_status": "BUSY",             // BUSY | TENTATIVE | FREE | OOF
  "recurrence_rule": "FREQ=WEEKLY;COUNT=4",
  "sequence": 0, "cal_name": "",
  "organizer": {"name": "Ada", "email": "ada@example.com"},
  "attendees": [{"name": "Bo", "email": "bo@example.com", "rsvp": true}],
  "geolocation": {"lat": 52.52, "lon": 13.405}
}`,
		Inputs:  "any items (one event file per item)",
		Outputs: "one item per input item carrying the .ics file (mime text/calendar)",
		Notes:   binaryNote + ` Read .ics files back with data.extract_from_file operation ics.`,
	},
}

// dataFilesDocOverrides replaces the existing data.compression entry (see
// upsertNodeDocs in catalog.go).
var dataFilesDocOverrides = []nodeDoc{
	{
		Type:     "data.compression",
		Category: "data",
		Short:    "Zip, tar, gzip: pack files into an archive or unpack one",
		Description: `compress packs the files in binary_property_name (comma list) into one zip,
tar or tar.gz archive, or gzips each one (output_format gzip). decompress
unpacks zip, tar, tar.gz and gzip files into one binary property per file
(<output_prefix>0, <output_prefix>1, …), up to 256 MiB in total per item.
The older operations gzip/gunzip/zip/unzip work on one base64 field
(input_field -> output_field) and are unchanged.`,
		Config: `{
  "operation": "compress",              // compress | decompress | gzip | gunzip | zip | unzip
  "binary_property_name": "data,report", // compress / decompress
  "output_format": "zip",               // zip | gzip | tar | targz
  "file_name": "archive.zip",
  "binary_property_output": "data",     // compress
  "output_prefix": "file_"              // decompress
}`,
		Inputs:  "items carrying files (compress/decompress) or a base64 field (legacy operations)",
		Outputs: "the item with the archive / extracted files in place of the input files",
		Notes:   binaryNote,
	},
}
