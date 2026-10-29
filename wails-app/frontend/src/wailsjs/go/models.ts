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
	
	export class AITask {
	    taskId: string;
	    kind: string;
	    conversationId: string;
	    status: string;
	    goal: string;
	    createdAt: string;
	    updatedAt: string;
	    applicationId: string;
	    error: string;
	    fallback: string;
	    reason: string;
	    result: any;
	
	    static createFrom(source: any = {}) {
	        return new AITask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.taskId = source["taskId"];
	        this.kind = source["kind"];
	        this.conversationId = source["conversationId"];
	        this.status = source["status"];
	        this.goal = source["goal"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.applicationId = source["applicationId"];
	        this.error = source["error"];
	        this.fallback = source["fallback"];
	        this.reason = source["reason"];
	        this.result = source["result"];
	    }
	}
	export class AITaskState {
	    enabled: boolean;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new AITaskState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.reason = source["reason"];
	    }
	}
	export class ChatPathInfo {
	    kind: string;
	    previewable: boolean;
	    path: string;
	    line: number;
	
	    static createFrom(source: any = {}) {
	        return new ChatPathInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.previewable = source["previewable"];
	        this.path = source["path"];
	        this.line = source["line"];
	    }
	}
	export class ParkedTab {
	    applicationId: string;
	    taskId: string;
	    tabId: number;
	    origin: string;
	    state: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new ParkedTab(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.applicationId = source["applicationId"];
	        this.taskId = source["taskId"];
	        this.tabId = source["tabId"];
	        this.origin = source["origin"];
	        this.state = source["state"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class ApplicationTab {
	    applicationId: string;
	    tab?: ParkedTab;
	    submitObservedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new ApplicationTab(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.applicationId = source["applicationId"];
	        this.tab = this.convertValues(source["tab"], ParkedTab);
	        this.submitObservedAt = source["submitObservedAt"];
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
	export class SweepResult {
	    closed: string[];
	    lost: string[];
	
	    static createFrom(source: any = {}) {
	        return new SweepResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.closed = source["closed"];
	        this.lost = source["lost"];
	    }
	}
	export class APIAuto {
	    available: boolean;
	    missing?: string;
	    key_source?: string;
	    confinement?: string;
	    candidates?: number;
	    held_back?: number;
	
	    static createFrom(source: any = {}) {
	        return new APIAuto(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.missing = source["missing"];
	        this.key_source = source["key_source"];
	        this.confinement = source["confinement"];
	        this.candidates = source["candidates"];
	        this.held_back = source["held_back"];
	    }
	}
	export class APIWidening {
	    key: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new APIWidening(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.reason = source["reason"];
	    }
	}
	export class APIConfigProblem {
	    key: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new APIConfigProblem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.message = source["message"];
	    }
	}
	export class APIConfigDaemon {
	    running: boolean;
	    reports_settings: boolean;
	    autostart: boolean;
	
	    static createFrom(source: any = {}) {
	        return new APIConfigDaemon(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.reports_settings = source["reports_settings"];
	        this.autostart = source["autostart"];
	    }
	}
	export class APIConfigSetting {
	    key: string;
	    server_flag: string;
	    env: string;
	    saved?: string;
	    default: string;
	    effective: string;
	    source: string;
	    running?: string;
	    running_source?: string;
	    state: string;
	
	    static createFrom(source: any = {}) {
	        return new APIConfigSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.server_flag = source["server_flag"];
	        this.env = source["env"];
	        this.saved = source["saved"];
	        this.default = source["default"];
	        this.effective = source["effective"];
	        this.source = source["source"];
	        this.running = source["running"];
	        this.running_source = source["running_source"];
	        this.state = source["state"];
	    }
	}
	export class APIConfigChange {
	    v: number;
	    environment: string;
	    settings: APIConfigSetting[];
	    daemon: APIConfigDaemon;
	    restart_needed: boolean;
	    problems: APIConfigProblem[];
	    applied: boolean;
	    changed: string[];
	    widening: APIWidening[];
	    removed_unreadable_row?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new APIConfigChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.v = source["v"];
	        this.environment = source["environment"];
	        this.settings = this.convertValues(source["settings"], APIConfigSetting);
	        this.daemon = this.convertValues(source["daemon"], APIConfigDaemon);
	        this.restart_needed = source["restart_needed"];
	        this.problems = this.convertValues(source["problems"], APIConfigProblem);
	        this.applied = source["applied"];
	        this.changed = source["changed"];
	        this.widening = this.convertValues(source["widening"], APIWidening);
	        this.removed_unreadable_row = source["removed_unreadable_row"];
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
	
	export class APIConfigInfo {
	    v: number;
	    environment: string;
	    settings: APIConfigSetting[];
	    daemon: APIConfigDaemon;
	    restart_needed: boolean;
	    problems: APIConfigProblem[];
	
	    static createFrom(source: any = {}) {
	        return new APIConfigInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.v = source["v"];
	        this.environment = source["environment"];
	        this.settings = this.convertValues(source["settings"], APIConfigSetting);
	        this.daemon = this.convertValues(source["daemon"], APIConfigDaemon);
	        this.restart_needed = source["restart_needed"];
	        this.problems = this.convertValues(source["problems"], APIConfigProblem);
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
	
	
	export class APIKey {
	    id: string;
	    profile_id: string;
	    name: string;
	    prefix: string;
	    context: boolean;
	    created_at: string;
	    last_used_at: string;
	    revoked_at: string;
	
	    static createFrom(source: any = {}) {
	        return new APIKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.profile_id = source["profile_id"];
	        this.name = source["name"];
	        this.prefix = source["prefix"];
	        this.context = source["context"];
	        this.created_at = source["created_at"];
	        this.last_used_at = source["last_used_at"];
	        this.revoked_at = source["revoked_at"];
	    }
	}
	export class APIKeyCreated {
	    id: string;
	    profile_id: string;
	    name: string;
	    prefix: string;
	    context: boolean;
	    created_at: string;
	    last_used_at: string;
	    revoked_at: string;
	    key: string;
	
	    static createFrom(source: any = {}) {
	        return new APIKeyCreated(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.profile_id = source["profile_id"];
	        this.name = source["name"];
	        this.prefix = source["prefix"];
	        this.context = source["context"];
	        this.created_at = source["created_at"];
	        this.last_used_at = source["last_used_at"];
	        this.revoked_at = source["revoked_at"];
	        this.key = source["key"];
	    }
	}
	export class APIListener {
	    name: string;
	    addr: string;
	    loopback: boolean;
	    v1: boolean;
	    confinement: string;
	    context_confinement: string;
	    auto_confinement?: string;
	    confinement_source: string;
	    scheme?: string;
	    reachable: boolean;
	    v1_answers: boolean;
	
	    static createFrom(source: any = {}) {
	        return new APIListener(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.addr = source["addr"];
	        this.loopback = source["loopback"];
	        this.v1 = source["v1"];
	        this.confinement = source["confinement"];
	        this.context_confinement = source["context_confinement"];
	        this.auto_confinement = source["auto_confinement"];
	        this.confinement_source = source["confinement_source"];
	        this.scheme = source["scheme"];
	        this.reachable = source["reachable"];
	        this.v1_answers = source["v1_answers"];
	    }
	}
	export class APIModel {
	    id: string;
	    runtime: string;
	    model: string;
	    label: string;
	    confinement: string;
	    validated: boolean;
	    allowed: boolean;
	    context_allowed: boolean;
	    auto_allowed?: boolean;
	    capabilities?: string[];
	
	    static createFrom(source: any = {}) {
	        return new APIModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.runtime = source["runtime"];
	        this.model = source["model"];
	        this.label = source["label"];
	        this.confinement = source["confinement"];
	        this.validated = source["validated"];
	        this.allowed = source["allowed"];
	        this.context_allowed = source["context_allowed"];
	        this.auto_allowed = source["auto_allowed"];
	        this.capabilities = source["capabilities"];
	    }
	}
	export class APIPolicy {
	    for: string;
	    confinement: string;
	    context_confinement: string;
	    auto_confinement?: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new APIPolicy(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.for = source["for"];
	        this.confinement = source["confinement"];
	        this.context_confinement = source["context_confinement"];
	        this.auto_confinement = source["auto_confinement"];
	        this.source = source["source"];
	    }
	}
	export class APIModelsInfo {
	    policy: APIPolicy;
	    models: APIModel[];
	    auto?: APIAuto;
	
	    static createFrom(source: any = {}) {
	        return new APIModelsInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.policy = this.convertValues(source["policy"], APIPolicy);
	        this.models = this.convertValues(source["models"], APIModel);
	        this.auto = this.convertValues(source["auto"], APIAuto);
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
	
	export class APIStatusDaemon {
	    running: boolean;
	    api_addr: string;
	    v1_addr: string;
	
	    static createFrom(source: any = {}) {
	        return new APIStatusDaemon(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.api_addr = source["api_addr"];
	        this.v1_addr = source["v1_addr"];
	    }
	}
	export class APIStatusKeys {
	    active: number;
	
	    static createFrom(source: any = {}) {
	        return new APIStatusKeys(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	    }
	}
	export class APIStatusInfo {
	    profile: string;
	    keys: APIStatusKeys;
	    daemon: APIStatusDaemon;
	    auto?: APIAuto;
	    listeners: APIListener[];
	
	    static createFrom(source: any = {}) {
	        return new APIStatusInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile = source["profile"];
	        this.keys = this.convertValues(source["keys"], APIStatusKeys);
	        this.daemon = this.convertValues(source["daemon"], APIStatusDaemon);
	        this.auto = this.convertValues(source["auto"], APIAuto);
	        this.listeners = this.convertValues(source["listeners"], APIListener);
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
	export class AssetAITask {
	    taskId: string;
	    conversationId: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetAITask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.taskId = source["taskId"];
	        this.conversationId = source["conversationId"];
	        this.status = source["status"];
	    }
	}
	export class AssetDocument {
	    id: string;
	    filename: string;
	    kind: string;
	    source: string;
	    format: string;
	    sizeBytes: number;
	    createdAt: string;
	    attached: boolean;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetDocument(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.filename = source["filename"];
	        this.kind = source["kind"];
	        this.source = source["source"];
	        this.format = source["format"];
	        this.sizeBytes = source["sizeBytes"];
	        this.createdAt = source["createdAt"];
	        this.attached = source["attached"];
	        this.path = source["path"];
	    }
	}
	export class AssetFillField {
	    label: string;
	    value: string;
	    source: string;
	    generated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AssetFillField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.value = source["value"];
	        this.source = source["source"];
	        this.generated = source["generated"];
	    }
	}
	export class AssetFill {
	    status: string;
	    screenshot: string;
	    updatedAt: string;
	    fields: AssetFillField[];
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetFill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.screenshot = source["screenshot"];
	        this.updatedAt = source["updatedAt"];
	        this.fields = this.convertValues(source["fields"], AssetFillField);
	        this.note = source["note"];
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
	export class AssetLink {
	    kind: string;
	    refId: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.refId = source["refId"];
	    }
	}
	export class AssetMessage {
	    id: string;
	    channel: string;
	    direction: string;
	    subject: string;
	    snippet: string;
	    at: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.channel = source["channel"];
	        this.direction = source["direction"];
	        this.subject = source["subject"];
	        this.snippet = source["snippet"];
	        this.at = source["at"];
	    }
	}
	export class AssetPerson {
	    id: string;
	    name: string;
	    role: string;
	    email: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetPerson(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.role = source["role"];
	        this.email = source["email"];
	    }
	}
	export class AssetPosting {
	    url: string;
	    capturedText: string;
	    capturedAt: string;
	    deadline: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetPosting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.capturedText = source["capturedText"];
	        this.capturedAt = source["capturedAt"];
	        this.deadline = source["deadline"];
	    }
	}
	export class AssetTask {
	    id: string;
	    title: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new AssetTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.status = source["status"];
	    }
	}
	export class ApplicationAssets {
	    posting: AssetPosting;
	    documents: AssetDocument[];
	    fill: AssetFill;
	    people: AssetPerson[];
	    messages: AssetMessage[];
	    tasks: AssetTask[];
	    task: AssetAITask;
	    links: AssetLink[];
	
	    static createFrom(source: any = {}) {
	        return new ApplicationAssets(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.posting = this.convertValues(source["posting"], AssetPosting);
	        this.documents = this.convertValues(source["documents"], AssetDocument);
	        this.fill = this.convertValues(source["fill"], AssetFill);
	        this.people = this.convertValues(source["people"], AssetPerson);
	        this.messages = this.convertValues(source["messages"], AssetMessage);
	        this.tasks = this.convertValues(source["tasks"], AssetTask);
	        this.task = this.convertValues(source["task"], AssetAITask);
	        this.links = this.convertValues(source["links"], AssetLink);
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
	export class BatchUploadResult {
	    uploaded: number;
	    unindexed: number;
	    failed: string[];
	
	    static createFrom(source: any = {}) {
	        return new BatchUploadResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uploaded = source["uploaded"];
	        this.unindexed = source["unindexed"];
	        this.failed = source["failed"];
	    }
	}
	export class BrowserRow {
	    instance: string;
	    label: string;
	    profile_id: string;
	    profile_name: string;
	    profile_ids: string[];
	    profile_names: string[];
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
	        this.profile_ids = source["profile_ids"];
	        this.profile_names = source["profile_names"];
	        this.legacy = source["legacy"];
	        this.conflict = source["conflict"];
	        this.version = source["version"];
	        this.connected_at = source["connected_at"];
	    }
	}
	export class AssignReport {
	    profile_id: string;
	    instance: string;
	    changed: BrowserRow[];

	    static createFrom(source: any = {}) {
	        return new AssignReport(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile_id = source["profile_id"];
	        this.instance = source["instance"];
	        this.changed = this.convertValues(source["changed"], BrowserRow);
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
	export class ExtensionInfo {
	    path: string;
	    installed: boolean;
	    current: boolean;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new ExtensionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.installed = source["installed"];
	        this.current = source["current"];
	        this.version = source["version"];
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
	export class CurlImport {
	    config: Record<string, any>;
	    notes: string[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new CurlImport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config = source["config"];
	        this.notes = source["notes"];
	        this.error = source["error"];
	    }
	}
	export class DaemonRestartResult {
	    restarted: boolean;
	    via: string;
	
	    static createFrom(source: any = {}) {
	        return new DaemonRestartResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.restarted = source["restarted"];
	        this.via = source["via"];
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
	export class ExcludedDocument {
	    path: string;
	    relative_path: string;
	    filename: string;
	    excluded_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ExcludedDocument(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.relative_path = source["relative_path"];
	        this.filename = source["filename"];
	        this.excluded_at = source["excluded_at"];
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
	export class AccessChange {
	    ok: boolean;
	    code: string;
	    current: string;
	    conversation: Conversation;
	    previousAccess: string;
	    lowered: LoweredEffects;
	
	    static createFrom(source: any = {}) {
	        return new AccessChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.code = source["code"];
	        this.current = source["current"];
	        this.conversation = this.convertValues(source["conversation"], Conversation);
	        this.previousAccess = source["previousAccess"];
	        this.lowered = this.convertValues(source["lowered"], LoweredEffects);
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
	export class ChatConversationRecord {
	    ok: boolean;
	    code: string;
	    id: string;
	    title: string;
	    access: string;
	    mode: string;
	    runtime: string;
	    model: string;
	    effort: string;
	    cwd: string;
	    orgMode: string;
	    workflowContext: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatConversationRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.code = source["code"];
	        this.id = source["id"];
	        this.title = source["title"];
	        this.access = source["access"];
	        this.mode = source["mode"];
	        this.runtime = source["runtime"];
	        this.model = source["model"];
	        this.effort = source["effort"];
	        this.cwd = source["cwd"];
	        this.orgMode = source["orgMode"];
	        this.workflowContext = source["workflowContext"];
	    }
	}
	export class Conversation {
	    id: string;
	    profileId: string;
	    backend: string;
	    workflowContext: string;
	    runtimeId: string;
	    model: string;
	    effort: string;
	    mode: string;
	    access: string;
	    cwd: string;
	    orgMode: string;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Conversation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.profileId = source["profileId"];
	        this.backend = source["backend"];
	        this.workflowContext = source["workflowContext"];
	        this.runtimeId = source["runtimeId"];
	        this.model = source["model"];
	        this.effort = source["effort"];
	        this.mode = source["mode"];
	        this.access = source["access"];
	        this.cwd = source["cwd"];
	        this.orgMode = source["orgMode"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class DeleteKept {
	    code: string;
	    message: string;
	    stopped: number[];
	    survivors: Survivor[];
	
	    static createFrom(source: any = {}) {
	        return new DeleteKept(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	        this.stopped = source["stopped"];
	        this.survivors = this.convertValues(source["survivors"], Survivor);
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
	export class DeleteChatResult {
	    deleted: string[];
	    active: string[];
	    failed: Record<string, string>;
	    kept: Record<string, DeleteKept>;
	
	    static createFrom(source: any = {}) {
	        return new DeleteChatResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deleted = source["deleted"];
	        this.active = source["active"];
	        this.failed = source["failed"];
	        this.kept = this.convertValues(source["kept"], DeleteKept, true);
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
	export class HistoryFilter {
	    kind: string;
	    status: string;
	    application: string;
	    query: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryFilter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.status = source["status"];
	        this.application = source["application"];
	        this.query = source["query"];
	    }
	}
	export class HistoryPage {
	    items: HistoryRow[];
	    next_cursor: string;
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new HistoryPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], HistoryRow);
	        this.next_cursor = source["next_cursor"];
	        this.warnings = source["warnings"];
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
	export class HistoryRow {
	    id: string;
	    kind: string;
	    title: string;
	    status: string;
	    updatedAt: string;
	    runtime: string;
	    model: string;
	    effort: string;
	    access: string;
	    mode: string;
	    orgMode: string;
	    cwd: string;
	    workflowContext: string;
	    taskId: string;
	    taskStatus: string;
	    taskGoal: string;
	    applicationId: string;
	    orgName: string;
	    runId: string;
	    costUsd: number;
	
	    static createFrom(source: any = {}) {
	        return new HistoryRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.updatedAt = source["updatedAt"];
	        this.runtime = source["runtime"];
	        this.model = source["model"];
	        this.effort = source["effort"];
	        this.access = source["access"];
	        this.mode = source["mode"];
	        this.orgMode = source["orgMode"];
	        this.cwd = source["cwd"];
	        this.workflowContext = source["workflowContext"];
	        this.taskId = source["taskId"];
	        this.taskStatus = source["taskStatus"];
	        this.taskGoal = source["taskGoal"];
	        this.applicationId = source["applicationId"];
	        this.orgName = source["orgName"];
	        this.runId = source["runId"];
	        this.costUsd = source["costUsd"];
	    }
	}
	export class LoweredEffects {
	    stopped: number[];
	    survivors: Survivor[];
	    workersDropped: boolean;
	    taskRevoked: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LoweredEffects(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stopped = source["stopped"];
	        this.survivors = this.convertValues(source["survivors"], Survivor);
	        this.workersDropped = source["workersDropped"];
	        this.taskRevoked = source["taskRevoked"];
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
	export class Survivor {
	    pid: number;
	    command: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new Survivor(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.command = source["command"];
	        this.reason = source["reason"];
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
	    person_id?: string;
	    person_full_name?: string;
	    hil_ref?: string;
	
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
	        this.person_id = source["person_id"];
	        this.person_full_name = source["person_full_name"];
	        this.hil_ref = source["hil_ref"];
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
	    kind: string;
	    own_account: boolean;
	
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
	        this.kind = source["kind"];
	        this.own_account = source["own_account"];
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
	    sent_at?: string;
	    created_at: string;
	    read_at?: string;
	
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
	        this.sent_at = source["sent_at"];
	        this.created_at = source["created_at"];
	        this.read_at = source["read_at"];
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
	    sent_at?: string;
	    created_at: string;
	    read_at?: string;
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
	        this.sent_at = source["sent_at"];
	        this.created_at = source["created_at"];
	        this.read_at = source["read_at"];
	        this.person_full_name = source["person_full_name"];
	        this.person_platform_username = source["person_platform_username"];
	        this.person_platform = source["person_platform"];
	    }
	}
	export class PersonStatusUpdate {
	    id: string;
	    person_id: string;
	    text: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new PersonStatusUpdate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.person_id = source["person_id"];
	        this.text = source["text"];
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
	export class ProfileTableCount {
	    table: string;
	    rows: number;
	
	    static createFrom(source: any = {}) {
	        return new ProfileTableCount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.table = source["table"];
	        this.rows = source["rows"];
	    }
	}
	export class ProfileDeleteSummary {
	    tables: ProfileTableCount[];
	    folder: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileDeleteSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tables = this.convertValues(source["tables"], ProfileTableCount);
	        this.folder = source["folder"];
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
	    tags: TagInfo[];
	
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
	        this.tags = this.convertValues(source["tags"], TagInfo);
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
	export class ProfileInfoFilled {
	    key: string;
	    origin: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoFilled(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.origin = source["origin"];
	    }
	}
	export class ProfileInfoAIFillResult {
	    asked: number;
	    filled: ProfileInfoFilled[];
	    skipped: number;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoAIFillResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.asked = source["asked"];
	        this.filled = this.convertValues(source["filled"], ProfileInfoFilled);
	        this.skipped = source["skipped"];
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
	export class ProfileInfoField {
	    key: string;
	    label: string;
	    group: string;
	    kind: string;
	    options: string[];
	    sensitive: boolean;
	    card: boolean;
	    autocomplete: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.group = source["group"];
	        this.kind = source["kind"];
	        this.options = source["options"];
	        this.sensitive = source["sensitive"];
	        this.card = source["card"];
	        this.autocomplete = source["autocomplete"];
	    }
	}
	export class ProfileInfoGroup {
	    id: string;
	    label: string;
	    scope: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.scope = source["scope"];
	    }
	}
	export class ProfileInfoCatalog {
	    groups: ProfileInfoGroup[];
	    fields: ProfileInfoField[];
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoCatalog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.groups = this.convertValues(source["groups"], ProfileInfoGroup);
	        this.fields = this.convertValues(source["fields"], ProfileInfoField);
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
	export class ProfileInfoEntry {
	    key: string;
	    value: string;
	    masked: boolean;
	    source: string;
	    origin: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.value = source["value"];
	        this.masked = source["masked"];
	        this.source = source["source"];
	        this.origin = source["origin"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ProfileInfoData {
	    profile: string;
	    entries: ProfileInfoEntry[];
	
	    static createFrom(source: any = {}) {
	        return new ProfileInfoData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile = source["profile"];
	        this.entries = this.convertValues(source["entries"], ProfileInfoEntry);
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
	export class NodeSubInput {
	    handle: string;
	    label: string;
	    max?: number;
	    required?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new NodeSubInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.handle = source["handle"];
	        this.label = source["label"];
	        this.max = source["max"];
	        this.required = source["required"];
	    }
	}
	export class NodeSchemaOutput {
	    key: string;
	    type?: string;
	    description?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeSchemaOutput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.type = source["type"];
	        this.description = source["description"];
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
	    visible_for?: any[];
	
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
	        this.visible_for = source["visible_for"];
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
	    outputs?: NodeSchemaOutput[];
	    sub_inputs?: NodeSubInput[];
	    sub_output?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeSchema(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.credential_platform = source["credential_platform"];
	        this.fields = this.convertValues(source["fields"], NodeSchemaField);
	        this.outputs = this.convertValues(source["outputs"], NodeSchemaOutput);
	        this.sub_inputs = this.convertValues(source["sub_inputs"], NodeSubInput);
	        this.sub_output = source["sub_output"];
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
	    summary?: string;
	    inputs: string[];
	
	    static createFrom(source: any = {}) {
	        return new Template(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.summary = source["summary"];
	        this.inputs = source["inputs"];
	    }
	}

}

