export namespace connections {
	
	export class SafeConnection {
	    id: string;
	    platform: string;
	    method: string;
	    label: string;
	    account_id: string;
	    status: string;
	    last_tested?: string;
	    profile_id?: string;
	    vault_ref?: string;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new SafeConnection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.platform = source["platform"];
	        this.method = source["method"];
	        this.label = source["label"];
	        this.account_id = source["account_id"];
	        this.status = source["status"];
	        this.last_tested = source["last_tested"];
	        this.profile_id = source["profile_id"];
	        this.vault_ref = source["vault_ref"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}

}

export namespace main {
	
	export class StatusLogEntry {
	    from_status: string;
	    to_status: string;
	    actor: string;
	    note: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusLogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from_status = source["from_status"];
	        this.to_status = source["to_status"];
	        this.actor = source["actor"];
	        this.note = source["note"];
	        this.created_at = source["created_at"];
	    }
	}
	export class ApplicationDetail {
	    id: string;
	    kind: string;
	    status: string;
	    title: string;
	    company: string;
	    url: string;
	    updated_at: string;
	    tags: string[];
	    description: string;
	    location: string;
	    job_type: string;
	    issuing_org: string;
	    submission_deadline: string;
	    status_log: StatusLogEntry[];
	
	    static createFrom(source: any = {}) {
	        return new ApplicationDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.status = source["status"];
	        this.title = source["title"];
	        this.company = source["company"];
	        this.url = source["url"];
	        this.updated_at = source["updated_at"];
	        this.tags = source["tags"];
	        this.description = source["description"];
	        this.location = source["location"];
	        this.job_type = source["job_type"];
	        this.issuing_org = source["issuing_org"];
	        this.submission_deadline = source["submission_deadline"];
	        this.status_log = this.convertValues(source["status_log"], StatusLogEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ApplicationSummary {
	    id: string;
	    kind: string;
	    status: string;
	    title: string;
	    company: string;
	    url: string;
	    updated_at: string;
	    tags: string[];
	
	    static createFrom(source: any = {}) {
	        return new ApplicationSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.status = source["status"];
	        this.title = source["title"];
	        this.company = source["company"];
	        this.url = source["url"];
	        this.updated_at = source["updated_at"];
	        this.tags = source["tags"];
	    }
	}
	export class ApplyResult {
	    cv_html_document_id: string;
	    cv_pdf_document_id: string;
	    cover_letter_html_document_id: string;
	    cover_letter_pdf_document_id: string;
	
	    static createFrom(source: any = {}) {
	        return new ApplyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cv_html_document_id = source["cv_html_document_id"];
	        this.cv_pdf_document_id = source["cv_pdf_document_id"];
	        this.cover_letter_html_document_id = source["cover_letter_html_document_id"];
	        this.cover_letter_pdf_document_id = source["cover_letter_pdf_document_id"];
	    }
	}
	export class BrowserRow {
	    instance: string;
	    label: string;
	    profile_id: string;
	    profile_name: string;
	    legacy: boolean;
	    conflict: boolean;
	    version: string;
	    connected_at: string;
	
	    static createFrom(source: any = {}) {
	        return new BrowserRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.instance = source["instance"];
	        this.label = source["label"];
	        this.profile_id = source["profile_id"];
	        this.profile_name = source["profile_name"];
	        this.legacy = source["legacy"];
	        this.conflict = source["conflict"];
	        this.version = source["version"];
	        this.connected_at = source["connected_at"];
	    }
	}
	export class BrowsersReport {
	    running: boolean;
	    hint: string;
	    browsers: BrowserRow[];
	
	    static createFrom(source: any = {}) {
	        return new BrowsersReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.hint = source["hint"];
	        this.browsers = this.convertValues(source["browsers"], BrowserRow);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CaptureSummaryStatus {
	    status: string;
	    runtime: string;
	    error: string;
	    finished_at: string;
	
	    static createFrom(source: any = {}) {
	        return new CaptureSummaryStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.runtime = source["runtime"];
	        this.error = source["error"];
	        this.finished_at = source["finished_at"];
	    }
	}
	export class CaptureView {
	    title: string;
	    url: string;
	    captured_at: string;
	    word_count: number;
	    readable: string;
	    screenshot: string;
	    summary: string;
	    transcript: string;
	    summary_status?: CaptureSummaryStatus;
	
	    static createFrom(source: any = {}) {
	        return new CaptureView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.url = source["url"];
	        this.captured_at = source["captured_at"];
	        this.word_count = source["word_count"];
	        this.readable = source["readable"];
	        this.screenshot = source["screenshot"];
	        this.summary = source["summary"];
	        this.transcript = source["transcript"];
	        this.summary_status = this.convertValues(source["summary_status"], CaptureSummaryStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CredentialOption {
	    id: string;
	    label: string;
	    platform: string;
	    method: string;
	
	    static createFrom(source: any = {}) {
	        return new CredentialOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.platform = source["platform"];
	        this.method = source["method"];
	    }
	}
	export class SessionSummary {
	    platform: string;
	    username: string;
	    expiry: string;
	    active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.platform = source["platform"];
	        this.username = source["username"];
	        this.expiry = source["expiry"];
	        this.active = source["active"];
	    }
	}
	export class DashboardStats {
	    active_sessions: number;
	    total_workflows: number;
	    executions_by_status: Record<string, number>;
	    total_people: number;
	    total_lists: number;
	    sessions: SessionSummary[];
	    db_path: string;
	
	    static createFrom(source: any = {}) {
	        return new DashboardStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_sessions = source["active_sessions"];
	        this.total_workflows = source["total_workflows"];
	        this.executions_by_status = source["executions_by_status"];
	        this.total_people = source["total_people"];
	        this.total_lists = source["total_lists"];
	        this.sessions = this.convertValues(source["sessions"], SessionSummary);
	        this.db_path = source["db_path"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EvaluateBatchResult {
	    evaluated: number;
	    verdicts: Record<string, number>;
	
	    static createFrom(source: any = {}) {
	        return new EvaluateBatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.evaluated = source["evaluated"];
	        this.verdicts = source["verdicts"];
	    }
	}
	export class ExportResult {
	    output_dir: string;
	    people_count: number;
	    cancelled?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.output_dir = source["output_dir"];
	        this.people_count = source["people_count"];
	        this.cancelled = source["cancelled"];
	    }
	}
	export class FitVerdictInfo {
	    eligibility_pass: boolean;
	    language_pass: boolean;
	    location_pass: boolean;
	    technical_score: number;
	    experience_score: number;
	    behavioral_score: number;
	    career_score: number;
	    overall_score: number;
	    verdict: string;
	    rationale: string;
	
	    static createFrom(source: any = {}) {
	        return new FitVerdictInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.eligibility_pass = source["eligibility_pass"];
	        this.language_pass = source["language_pass"];
	        this.location_pass = source["location_pass"];
	        this.technical_score = source["technical_score"];
	        this.experience_score = source["experience_score"];
	        this.behavioral_score = source["behavioral_score"];
	        this.career_score = source["career_score"];
	        this.overall_score = source["overall_score"];
	        this.verdict = source["verdict"];
	        this.rationale = source["rationale"];
	    }
	}
	export class HILItem {
	    id: string;
	    execution_id: string;
	    workflow_id: string;
	    workflow_name: string;
	    node_id: string;
	    node_name: string;
	    status: string;
	    readonly_data: Record<string, any>;
	    editable_data: Record<string, any>;
	    node_config: Record<string, any>;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new HILItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.execution_id = source["execution_id"];
	        this.workflow_id = source["workflow_id"];
	        this.workflow_name = source["workflow_name"];
	        this.node_id = source["node_id"];
	        this.node_name = source["node_name"];
	        this.status = source["status"];
	        this.readonly_data = source["readonly_data"];
	        this.editable_data = source["editable_data"];
	        this.node_config = source["node_config"];
	        this.created_at = source["created_at"];
	    }
	}
	export class JevKeyRemoveResult {
	    removed: string;
	
	    static createFrom(source: any = {}) {
	        return new JevKeyRemoveResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.removed = source["removed"];
	    }
	}
	export class JevKeySetResult {
	    key_source: string;
	    key_entry: string;
	    replaced: boolean;
	
	    static createFrom(source: any = {}) {
	        return new JevKeySetResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key_source = source["key_source"];
	        this.key_entry = source["key_entry"];
	        this.replaced = source["replaced"];
	    }
	}
	export class JevKeyTestResult {
	    ok: boolean;
	    key_source: string;
	    models: string[];
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new JevKeyTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.key_source = source["key_source"];
	        this.models = source["models"];
	        this.error = source["error"];
	    }
	}
	export class JevSurface {
	    surface: string;
	    title: string;
	    description: string;
	    egress: string[];
	    enabled: boolean;
	    threshold: number;
	    default_threshold: number;
	
	    static createFrom(source: any = {}) {
	        return new JevSurface(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.surface = source["surface"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.egress = source["egress"];
	        this.enabled = source["enabled"];
	        this.threshold = source["threshold"];
	        this.default_threshold = source["default_threshold"];
	    }
	}
	export class JevStatusInfo {
	    profile_id: string;
	    key_source: string;
	    key_entry: string;
	    model: string;
	    base_url: string;
	    surfaces: JevSurface[];
	
	    static createFrom(source: any = {}) {
	        return new JevStatusInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile_id = source["profile_id"];
	        this.key_source = source["key_source"];
	        this.key_entry = source["key_entry"];
	        this.model = source["model"];
	        this.base_url = source["base_url"];
	        this.surfaces = this.convertValues(source["surfaces"], JevSurface);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class JevSurfaceResult {
	    profile_id: string;
	    surface: string;
	    enabled: boolean;
	    threshold: number;
	    egress: string[];
	
	    static createFrom(source: any = {}) {
	        return new JevSurfaceResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile_id = source["profile_id"];
	        this.surface = source["surface"];
	        this.enabled = source["enabled"];
	        this.threshold = source["threshold"];
	        this.egress = source["egress"];
	    }
	}
	export class JevUsageRow {
	    surface: string;
	    calls: number;
	    failures: number;
	    input_tokens: number;
	    estimated_usd: number;
	    avg_latency_ms: number;
	
	    static createFrom(source: any = {}) {
	        return new JevUsageRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.surface = source["surface"];
	        this.calls = source["calls"];
	        this.failures = source["failures"];
	        this.input_tokens = source["input_tokens"];
	        this.estimated_usd = source["estimated_usd"];
	        this.avg_latency_ms = source["avg_latency_ms"];
	    }
	}
	export class JevUsageReport {
	    profile_id: string;
	    since: string;
	    surfaces: JevUsageRow[];
	    total: JevUsageRow;
	
	    static createFrom(source: any = {}) {
	        return new JevUsageReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile_id = source["profile_id"];
	        this.since = source["since"];
	        this.surfaces = this.convertValues(source["surfaces"], JevUsageRow);
	        this.total = this.convertValues(source["total"], JevUsageRow);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class KeyringPassphraseFile {
	    source: string;
	    configured: boolean;
	    path: string;
	    exists: boolean;
	    mode: string;
	    ok: boolean;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new KeyringPassphraseFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.configured = source["configured"];
	        this.path = source["path"];
	        this.exists = source["exists"];
	        this.mode = source["mode"];
	        this.ok = source["ok"];
	        this.error = source["error"];
	    }
	}
	export class KeyringPassphraseResult {
	    path: string;
	    saved: boolean;
	    removed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new KeyringPassphraseResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.saved = source["saved"];
	        this.removed = source["removed"];
	    }
	}
	export class KeyringStatusInfo {
	    backend: string;
	    os_keyring_error: string;
	    file_keyring_allowed: boolean;
	    file_keyrings: string[];
	    passphrase_file: KeyringPassphraseFile;
	
	    static createFrom(source: any = {}) {
	        return new KeyringStatusInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.backend = source["backend"];
	        this.os_keyring_error = source["os_keyring_error"];
	        this.file_keyring_allowed = source["file_keyring_allowed"];
	        this.file_keyrings = source["file_keyrings"];
	        this.passphrase_file = this.convertValues(source["passphrase_file"], KeyringPassphraseFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class KnowledgeSearchResult {
	    path: string;
	    excerpt: string;
	    score: number;
	
	    static createFrom(source: any = {}) {
	        return new KnowledgeSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.excerpt = source["excerpt"];
	        this.score = source["score"];
	    }
	}
	export class LogEntry {
	    time: string;
	    source: string;
	    level: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.source = source["source"];
	        this.level = source["level"];
	        this.message = source["message"];
	    }
	}
	export class MonomindProjectInfo {
	    path: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new MonomindProjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	    }
	}
	export class NodeRunOutput {
	    handle: string;
	    items: any[];
	
	    static createFrom(source: any = {}) {
	        return new NodeRunOutput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.handle = source["handle"];
	        this.items = source["items"];
	    }
	}
	export class NodeRunRequest {
	    node_type: string;
	    config: Record<string, any>;
	    items: any[];
	
	    static createFrom(source: any = {}) {
	        return new NodeRunRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_type = source["node_type"];
	        this.config = source["config"];
	        this.items = source["items"];
	    }
	}
	export class NodeRunResult {
	    outputs: NodeRunOutput[];
	    error?: string;
	    duration_ms: number;
	    run_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeRunResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.outputs = this.convertValues(source["outputs"], NodeRunOutput);
	        this.error = source["error"];
	        this.duration_ms = source["duration_ms"];
	        this.run_id = source["run_id"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PendingPersonApproval {
	    id: string;
	    platform: string;
	    platform_username: string;
	    full_name: string;
	    image_url: string;
	    profile_url: string;
	    job_title: string;
	    category: string;
	    introduction: string;
	    created_at: string;
	    suggestion?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new PendingPersonApproval(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.platform = source["platform"];
	        this.platform_username = source["platform_username"];
	        this.full_name = source["full_name"];
	        this.image_url = source["image_url"];
	        this.profile_url = source["profile_url"];
	        this.job_title = source["job_title"];
	        this.category = source["category"];
	        this.introduction = source["introduction"];
	        this.created_at = source["created_at"];
	        this.suggestion = source["suggestion"];
	    }
	}
	export class PersonDetailInfo {
	    id: string;
	    username: string;
	    platform: string;
	    full_name: string;
	    image_url: string;
	    profile_url: string;
	    follower_count: string;
	    following_count: number;
	    content_count: number;
	    is_verified: boolean;
	    job_title: string;
	    category: string;
	    introduction: string;
	    website: string;
	    contact_details: string;
	    created_at: string;
	    updated_at: string;
	    headline: string;
	    location: string;
	    about: string;
	    experience: any[];
	    education: any[];
	    profile_details: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new PersonDetailInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.username = source["username"];
	        this.platform = source["platform"];
	        this.full_name = source["full_name"];
	        this.image_url = source["image_url"];
	        this.profile_url = source["profile_url"];
	        this.follower_count = source["follower_count"];
	        this.following_count = source["following_count"];
	        this.content_count = source["content_count"];
	        this.is_verified = source["is_verified"];
	        this.job_title = source["job_title"];
	        this.category = source["category"];
	        this.introduction = source["introduction"];
	        this.website = source["website"];
	        this.contact_details = source["contact_details"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.headline = source["headline"];
	        this.location = source["location"];
	        this.about = source["about"];
	        this.experience = source["experience"];
	        this.education = source["education"];
	        this.profile_details = source["profile_details"];
	    }
	}
	export class PersonInfo {
	    id: string;
	    username: string;
	    platform: string;
	    full_name: string;
	    image_url: string;
	    profile_url: string;
	    follower_count: string;
	    following_count: number;
	    is_verified: boolean;
	    job_title: string;
	    category: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new PersonInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.username = source["username"];
	        this.platform = source["platform"];
	        this.full_name = source["full_name"];
	        this.image_url = source["image_url"];
	        this.profile_url = source["profile_url"];
	        this.follower_count = source["follower_count"];
	        this.following_count = source["following_count"];
	        this.is_verified = source["is_verified"];
	        this.job_title = source["job_title"];
	        this.category = source["category"];
	        this.created_at = source["created_at"];
	    }
	}
	export class PersonInteraction {
	    execution_id: string;
	    node_name: string;
	    node_type: string;
	    platform: string;
	    link: string;
	    status: string;
	    comment_text: string;
	    last_interacted_at: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new PersonInteraction(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.execution_id = source["execution_id"];
	        this.node_name = source["node_name"];
	        this.node_type = source["node_type"];
	        this.platform = source["platform"];
	        this.link = source["link"];
	        this.status = source["status"];
	        this.comment_text = source["comment_text"];
	        this.last_interacted_at = source["last_interacted_at"];
	        this.created_at = source["created_at"];
	    }
	}
	export class PostComment {
	    id: string;
	    author: string;
	    text: string;
	    timestamp: string;
	    likes_count: number;
	    reply_count: number;
	
	    static createFrom(source: any = {}) {
	        return new PostComment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.author = source["author"];
	        this.text = source["text"];
	        this.timestamp = source["timestamp"];
	        this.likes_count = source["likes_count"];
	        this.reply_count = source["reply_count"];
	    }
	}
	export class PostDetail {
	    id: string;
	    shortcode: string;
	    url: string;
	    thumbnail_url: string;
	    like_count: number;
	    comment_count: number;
	    caption: string;
	    posted_at: string;
	    scraped_at: string;
	
	    static createFrom(source: any = {}) {
	        return new PostDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.shortcode = source["shortcode"];
	        this.url = source["url"];
	        this.thumbnail_url = source["thumbnail_url"];
	        this.like_count = source["like_count"];
	        this.comment_count = source["comment_count"];
	        this.caption = source["caption"];
	        this.posted_at = source["posted_at"];
	        this.scraped_at = source["scraped_at"];
	    }
	}
	export class PostSummary {
	    id: string;
	    shortcode: string;
	    url: string;
	    thumbnail_url: string;
	    like_count: number;
	    comment_count: number;
	    caption: string;
	    posted_at: string;
	    scraped_at: string;
	    we_liked: boolean;
	    we_commented: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PostSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.shortcode = source["shortcode"];
	        this.url = source["url"];
	        this.thumbnail_url = source["thumbnail_url"];
	        this.like_count = source["like_count"];
	        this.comment_count = source["comment_count"];
	        this.caption = source["caption"];
	        this.posted_at = source["posted_at"];
	        this.scraped_at = source["scraped_at"];
	        this.we_liked = source["we_liked"];
	        this.we_commented = source["we_commented"];
	    }
	}
	export class ProfileDocument {
	    id: string;
	    filename: string;
	    path: string;
	    size_bytes: number;
	    source: string;
	    application_id: string;
	    created_at: string;
	    indexed: boolean;
	    index_error: string;
	    stale: boolean;
	    url: string;
	    capture_dir: string;
	    summary_status: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileDocument(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.filename = source["filename"];
	        this.path = source["path"];
	        this.size_bytes = source["size_bytes"];
	        this.source = source["source"];
	        this.application_id = source["application_id"];
	        this.created_at = source["created_at"];
	        this.indexed = source["indexed"];
	        this.index_error = source["index_error"];
	        this.stale = source["stale"];
	        this.url = source["url"];
	        this.capture_dir = source["capture_dir"];
	        this.summary_status = source["summary_status"];
	    }
	}
	export class ProfileInfo {
	    id: string;
	    name: string;
	    is_active: boolean;
	    created_at: string;
	    root_dir: string;
	    icon: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.is_active = source["is_active"];
	        this.created_at = source["created_at"];
	        this.root_dir = source["root_dir"];
	        this.icon = source["icon"];
	    }
	}
	export class ResourceItem {
	    id: string;
	    name: string;
	    description?: string;
	    metadata?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ResourceItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.metadata = source["metadata"];
	    }
	}
	export class ResourceItemResult {
	    item?: ResourceItem;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ResourceItemResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.item = this.convertValues(source["item"], ResourceItem);
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ResourceListResult {
	    items: ResourceItem[];
	    next_cursor?: string;
	    error?: string;
	    needs_reauth?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResourceListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], ResourceItem);
	        this.next_cursor = source["next_cursor"];
	        this.error = source["error"];
	        this.needs_reauth = source["needs_reauth"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkflowConnectionData {
	    id: string;
	    source_node_id: string;
	    source_handle: string;
	    target_node_id: string;
	    target_handle: string;
	    position: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkflowConnectionData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source_node_id = source["source_node_id"];
	        this.source_handle = source["source_handle"];
	        this.target_node_id = source["target_node_id"];
	        this.target_handle = source["target_handle"];
	        this.position = source["position"];
	    }
	}
	export class WorkflowNodeData {
	    id: string;
	    node_type: string;
	    name: string;
	    config: Record<string, any>;
	    position_x: number;
	    position_y: number;
	    disabled: boolean;
	    schema?: workflow.NodeSchema;
	
	    static createFrom(source: any = {}) {
	        return new WorkflowNodeData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.node_type = source["node_type"];
	        this.name = source["name"];
	        this.config = source["config"];
	        this.position_x = source["position_x"];
	        this.position_y = source["position_y"];
	        this.disabled = source["disabled"];
	        this.schema = this.convertValues(source["schema"], workflow.NodeSchema);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SaveWorkflowRequest {
	    id: string;
	    name: string;
	    description: string;
	    is_active: boolean;
	    nodes: WorkflowNodeData[];
	    connections: WorkflowConnectionData[];
	
	    static createFrom(source: any = {}) {
	        return new SaveWorkflowRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.is_active = source["is_active"];
	        this.nodes = this.convertValues(source["nodes"], WorkflowNodeData);
	        this.connections = this.convertValues(source["connections"], WorkflowConnectionData);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionInfo {
	    id: number;
	    username: string;
	    platform: string;
	    expiry: string;
	    added_at: string;
	    active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.username = source["username"];
	        this.platform = source["platform"];
	        this.expiry = source["expiry"];
	        this.added_at = source["added_at"];
	        this.active = source["active"];
	    }
	}
	
	export class SocialListInfo {
	    id: string;
	    name: string;
	    list_type: string;
	    item_count: number;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new SocialListInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.list_type = source["list_type"];
	        this.item_count = source["item_count"];
	        this.created_at = source["created_at"];
	    }
	}
	
	export class TagInfo {
	    id: string;
	    name: string;
	    color: string;
	
	    static createFrom(source: any = {}) {
	        return new TagInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	    }
	}
	export class TemplateInfo {
	    id: number;
	    name: string;
	    subject: string;
	    body: string;
	
	    static createFrom(source: any = {}) {
	        return new TemplateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.subject = source["subject"];
	        this.body = source["body"];
	    }
	}
	export class UpdateInfo {
	    current_version: string;
	    latest_version: string;
	    update_available: boolean;
	    release_url: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current_version = source["current_version"];
	        this.latest_version = source["latest_version"];
	        this.update_available = source["update_available"];
	        this.release_url = source["release_url"];
	        this.error = source["error"];
	    }
	}
	export class UpdateResult {
	    success: boolean;
	    up_to_date?: boolean;
	    new_version?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.up_to_date = source["up_to_date"];
	        this.new_version = source["new_version"];
	        this.error = source["error"];
	    }
	}
	export class UploadResult {
	    id: string;
	    indexed: boolean;
	    index_error: string;
	
	    static createFrom(source: any = {}) {
	        return new UploadResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.indexed = source["indexed"];
	        this.index_error = source["index_error"];
	    }
	}
	export class VaultEntry {
	    id: string;
	    profile_id: string;
	    kind: string;
	    name: string;
	    username?: string;
	    url?: string;
	    field_count: number;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new VaultEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.profile_id = source["profile_id"];
	        this.kind = source["kind"];
	        this.name = source["name"];
	        this.username = source["username"];
	        this.url = source["url"];
	        this.field_count = source["field_count"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class VaultExportResult {
	    path: string;
	    passphrase: string;
	    exported: number;
	    skipped: number;
	    cancelled?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VaultExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.passphrase = source["passphrase"];
	        this.exported = source["exported"];
	        this.skipped = source["skipped"];
	        this.cancelled = source["cancelled"];
	    }
	}
	export class VaultFieldsAndNotes {
	    fields: Record<string, string>;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new VaultFieldsAndNotes(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fields = source["fields"];
	        this.notes = source["notes"];
	    }
	}
	export class VaultImportResult {
	    imported: number;
	    skipped: number;
	
	    static createFrom(source: any = {}) {
	        return new VaultImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imported = source["imported"];
	        this.skipped = source["skipped"];
	    }
	}
	export class VersionInfo {
	    version: string;
	    build_date: string;
	
	    static createFrom(source: any = {}) {
	        return new VersionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.build_date = source["build_date"];
	    }
	}
	
	export class WorkflowDetail {
	    id: string;
	    name: string;
	    description: string;
	    is_active: boolean;
	    version: number;
	    created_at: string;
	    updated_at: string;
	    node_count: number;
	    profile_id?: string;
	    profile_name?: string;
	    nodes: WorkflowNodeData[];
	    connections: WorkflowConnectionData[];
	
	    static createFrom(source: any = {}) {
	        return new WorkflowDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.is_active = source["is_active"];
	        this.version = source["version"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.node_count = source["node_count"];
	        this.profile_id = source["profile_id"];
	        this.profile_name = source["profile_name"];
	        this.nodes = this.convertValues(source["nodes"], WorkflowNodeData);
	        this.connections = this.convertValues(source["connections"], WorkflowConnectionData);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkflowExecutionSummary {
	    id: string;
	    workflow_id: string;
	    workflow_name: string;
	    status: string;
	    trigger_type: string;
	    started_at: string;
	    finished_at: string;
	    error: string;
	    created_at: string;
	    profile_id?: string;
	    profile_name?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkflowExecutionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workflow_id = source["workflow_id"];
	        this.workflow_name = source["workflow_name"];
	        this.status = source["status"];
	        this.trigger_type = source["trigger_type"];
	        this.started_at = source["started_at"];
	        this.finished_at = source["finished_at"];
	        this.error = source["error"];
	        this.created_at = source["created_at"];
	        this.profile_id = source["profile_id"];
	        this.profile_name = source["profile_name"];
	    }
	}
	export class WorkflowImportResult {
	    id: string;
	    name: string;
	    status?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkflowImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.status = source["status"];
	    }
	}
	
	export class WorkflowSummary {
	    id: string;
	    name: string;
	    description: string;
	    is_active: boolean;
	    version: number;
	    created_at: string;
	    updated_at: string;
	    node_count: number;
	    profile_id?: string;
	    profile_name?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkflowSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.is_active = source["is_active"];
	        this.version = source["version"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.node_count = source["node_count"];
	        this.profile_id = source["profile_id"];
	        this.profile_name = source["profile_name"];
	    }
	}

}

export namespace storage {
	
	export class PersonMessage {
	    id: string;
	    person_id: string;
	    source: string;
	    external_id?: string;
	    direction: string;
	    sender?: string;
	    subject?: string;
	    body?: string;
	    metadata?: string;
	    status?: string;
	    // Go type: time
	    sent_at?: any;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    read_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new PersonMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.person_id = source["person_id"];
	        this.source = source["source"];
	        this.external_id = source["external_id"];
	        this.direction = source["direction"];
	        this.sender = source["sender"];
	        this.subject = source["subject"];
	        this.body = source["body"];
	        this.metadata = source["metadata"];
	        this.status = source["status"];
	        this.sent_at = this.convertValues(source["sent_at"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.read_at = this.convertValues(source["read_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PersonMessageWithPerson {
	    id: string;
	    person_id: string;
	    source: string;
	    external_id?: string;
	    direction: string;
	    sender?: string;
	    subject?: string;
	    body?: string;
	    metadata?: string;
	    status?: string;
	    // Go type: time
	    sent_at?: any;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    read_at?: any;
	    person_full_name?: string;
	    person_platform_username: string;
	    person_platform: string;
	
	    static createFrom(source: any = {}) {
	        return new PersonMessageWithPerson(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.person_id = source["person_id"];
	        this.source = source["source"];
	        this.external_id = source["external_id"];
	        this.direction = source["direction"];
	        this.sender = source["sender"];
	        this.subject = source["subject"];
	        this.body = source["body"];
	        this.metadata = source["metadata"];
	        this.status = source["status"];
	        this.sent_at = this.convertValues(source["sent_at"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.read_at = this.convertValues(source["read_at"], null);
	        this.person_full_name = source["person_full_name"];
	        this.person_platform_username = source["person_platform_username"];
	        this.person_platform = source["person_platform"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PersonStatusUpdate {
	    id: string;
	    person_id: string;
	    text: string;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new PersonStatusUpdate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.person_id = source["person_id"];
	        this.text = source["text"];
	        this.created_at = this.convertValues(source["created_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace workflow {
	
	export class FieldDependency {
	    key: string;
	    values: string[];
	
	    static createFrom(source: any = {}) {
	        return new FieldDependency(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.values = source["values"];
	    }
	}
	export class ResourcePickerConfig {
	    type: string;
	    create_label?: string;
	    param_field?: string;
	
	    static createFrom(source: any = {}) {
	        return new ResourcePickerConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.create_label = source["create_label"];
	        this.param_field = source["param_field"];
	    }
	}
	export class NodeSchemaField {
	    key: string;
	    label: string;
	    type: string;
	    required: boolean;
	    default?: any;
	    placeholder?: string;
	    examples?: string[];
	    help?: string;
	    options?: string[];
	    language?: string;
	    rows?: number;
	    min?: number;
	    max?: number;
	    item_type?: string;
	    resource?: ResourcePickerConfig;
	    depends_on?: FieldDependency;
	
	    static createFrom(source: any = {}) {
	        return new NodeSchemaField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.type = source["type"];
	        this.required = source["required"];
	        this.default = source["default"];
	        this.placeholder = source["placeholder"];
	        this.examples = source["examples"];
	        this.help = source["help"];
	        this.options = source["options"];
	        this.language = source["language"];
	        this.rows = source["rows"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.item_type = source["item_type"];
	        this.resource = this.convertValues(source["resource"], ResourcePickerConfig);
	        this.depends_on = this.convertValues(source["depends_on"], FieldDependency);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NodeSchema {
	    credential_platform?: string;
	    fields: NodeSchemaField[];
	
	    static createFrom(source: any = {}) {
	        return new NodeSchema(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.credential_platform = source["credential_platform"];
	        this.fields = this.convertValues(source["fields"], NodeSchemaField);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class Template {
	    id: string;
	    name: string;
	    description: string;
	    inputs: string[];
	
	    static createFrom(source: any = {}) {
	        return new Template(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.inputs = source["inputs"];
	    }
	}

}

