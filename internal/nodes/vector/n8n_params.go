package vector

// n8n → mono mapping tables for the workflow importer (internal/n8nimport).
//
// n8n builds RAG pipelines out of LangChain sub-nodes: an embedding node
// hangs off a vector store through an ai_embedding connection, a document
// loader hangs off an inserting store through ai_document, and a text
// splitter hangs off the loader through ai_textSplitter. Mono's RAG nodes
// are flat, so the importer folds that sub-node tree into plain config:
//
//	n8n                                         mono
//	─────────────────────────────────────────   ─────────────────────────────
//	vectorStoreX ◄─ai_embedding── embeddingsY   vector.store {provider:x,
//	                                             embedding:{provider:y,…}}
//	vectorStoreX ◄─ai_document── loader         vector.load_document {…, split}
//	     loader  ◄─ai_textSplitter── splitter   → vector.store (inserted before)
//	retrieverVectorStore ◄─ai_vectorStore── X   vector.store {mode:search,top_k}
//
// CollapseN8nSubNodes (n8n_collapse.go) applies these rules; the tables
// below say how each node's parameters map.

// N8nPrefix is the package prefix of every n8n LangChain node type.
const N8nPrefix = "@n8n/n8n-nodes-langchain."

// N8n node roles.
const (
	N8nRoleNode      = "node"      // becomes a mono node of OurType
	N8nRoleEmbedding = "embedding" // folded into its parent's "embedding" block
	N8nRoleSplitter  = "splitter"  // folded into its parent loader's split settings
	N8nRoleRetriever = "retriever" // folded with its ai_vectorStore child
)

// N8nNodeMapping says how one n8n node type maps onto mono.
type N8nNodeMapping struct {
	Role    string
	OurType string // for N8nRoleNode (and retrievers: the type they become)
	// Fixed is constant config the mapping always sets (provider, mode …).
	Fixed map[string]interface{}
	// Params maps an n8n parameter path ("options.batchSize") to our
	// config key. Resource-locator values ({__rl, mode, value}) are
	// unwrapped to their value.
	Params map[string]string
	// Values rewrites enum values per our key: our key → n8n value → ours.
	Values map[string]map[string]interface{}
	// Coverage is "full", "partial" or "unsupported"; Notes says what is
	// lost for partial and why for unsupported.
	Coverage string
	Notes    string
}

// Shared parameter maps for vector stores (every store node has these).
var n8nStoreCommon = map[string]string{
	"mode":                     "mode",
	"topK":                     "top_k",
	"prompt":                   "query",
	"toolName":                 "tool_name",
	"toolDescription":          "tool_description",
	"embeddingBatchSize":       "upsert_batch_size",
	"id":                       "ids",
	"options.searchFilterJson": "filter",
}

func storeParams(extra map[string]string) map[string]string {
	out := make(map[string]string, len(n8nStoreCommon)+len(extra))
	for k, v := range n8nStoreCommon {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func storeNode(provider, mode string, extra map[string]string, coverage, notes string) N8nNodeMapping {
	fixed := map[string]interface{}{"provider": provider}
	if mode != "" {
		fixed["mode"] = mode
	}
	return N8nNodeMapping{Role: N8nRoleNode, OurType: "vector.store", Fixed: fixed,
		Params: storeParams(extra), Coverage: coverage, Notes: notes}
}

func embedNode(provider string, params map[string]string, coverage, notes string) N8nNodeMapping {
	return N8nNodeMapping{Role: N8nRoleEmbedding, OurType: "vector.embed",
		Fixed: map[string]interface{}{"provider": provider}, Params: params, Coverage: coverage, Notes: notes}
}

var (
	qdrantParams   = map[string]string{"qdrantCollection": "collection", "options.contentPayloadKey": "content_payload_key", "options.metadataPayloadKey": "metadata_payload_key"}
	pineconeParams = map[string]string{"pineconeIndex": "collection", "options.pineconeNamespace": "namespace", "pineconeNamespace": "namespace", "options.clearNamespace": "clear", "clearNamespace": "clear"}
	supabaseParams = map[string]string{"tableName": "collection", "options.queryName": "query_name", "queryName": "query_name"}
	inMemoryParams = map[string]string{"memoryKey": "memory_key", "clearStore": "clear"}
	zepNote        = "Zep's vector store is deprecated upstream and has no mono provider; re-point the workflow at another store"
)

// N8nNodeMappings is keyed by the full n8n node type.
var N8nNodeMappings = map[string]N8nNodeMapping{
	// ── embeddings ──────────────────────────────────────────────────────────
	N8nPrefix + "embeddingsOpenAi": embedNode(ProviderOpenAICompatible, map[string]string{
		"model": "model", "options.dimensions": "dimensions", "options.baseURL": "base_url", "options.batchSize": "batch_size",
	}, "full", ""),
	N8nPrefix + "embeddingsAzureOpenAi": embedNode(ProviderAzureOpenAI, map[string]string{
		"model": "model", "options.dimensions": "dimensions", "options.batchSize": "batch_size",
	}, "partial", "API-key auth only; Entra ID (OAuth2) auth is not supported"),
	N8nPrefix + "embeddingsOllama": embedNode(ProviderOllama, map[string]string{"model": "model"}, "full", ""),
	N8nPrefix + "embeddingsCohere": embedNode(ProviderCohere, map[string]string{"modelName": "model"}, "full", ""),
	N8nPrefix + "embeddingsMistralCloud": embedNode(ProviderMistral, map[string]string{
		"model": "model", "options.batchSize": "batch_size",
	}, "full", ""),
	N8nPrefix + "embeddingsGoogleGemini": embedNode(ProviderGemini, map[string]string{"modelName": "model"}, "full", ""),
	N8nPrefix + "embeddingsGoogleVertex": embedNode(ProviderGemini, map[string]string{"modelName": "model"}, "partial",
		"Vertex service-account auth is not supported; the node is mapped to the Gemini API, so connect a Gemini API key"),
	N8nPrefix + "embeddingsHuggingFaceInference": embedNode(ProviderHuggingFace, map[string]string{
		"modelName": "model", "options.endpointUrl": "base_url",
	}, "full", ""),
	N8nPrefix + "embeddingsAwsBedrock": embedNode(ProviderBedrock, map[string]string{"model": "model"}, "full", ""),
	N8nPrefix + "embeddingsNvidia": {Role: N8nRoleEmbedding, OurType: "vector.embed",
		Fixed:  map[string]interface{}{"provider": ProviderOpenAICompatible, "base_url": "https://integrate.api.nvidia.com/v1"},
		Params: map[string]string{"model": "model", "options.batchSize": "batch_size"}, Coverage: "full",
		Notes: "NVIDIA's API is OpenAI-compatible"},
	N8nPrefix + "embeddingsLemonade": {Role: N8nRoleEmbedding, OurType: "vector.embed",
		Fixed: map[string]interface{}{"provider": ProviderOpenAICompatible}, Params: map[string]string{"model": "model"},
		Coverage: "partial", Notes: "Lemonade serves an OpenAI-compatible API; set base_url to the server (e.g. http://localhost:8000/api/v1)"},
	N8nPrefix + "embeddingsDatabricks": {Role: N8nRoleEmbedding, Coverage: "unsupported", Notes: "no Databricks provider"},
	N8nPrefix + "embeddingsOracleDb":   {Role: N8nRoleEmbedding, Coverage: "unsupported", Notes: "no Oracle DB provider"},

	// ── vector stores ───────────────────────────────────────────────────────
	N8nPrefix + "vectorStoreQdrant":         storeNode(StoreQdrant, "", qdrantParams, "partial", "options.collectionConfig (custom collection creation) is not mapped; collections are created with the vector size and Cosine distance"),
	N8nPrefix + "vectorStorePinecone":       storeNode(StorePinecone, "", pineconeParams, "full", ""),
	N8nPrefix + "vectorStorePineconeInsert": storeNode(StorePinecone, "insert", pineconeParams, "full", ""),
	N8nPrefix + "vectorStorePineconeLoad":   storeNode(StorePinecone, "search", pineconeParams, "full", ""),
	N8nPrefix + "vectorStoreSupabase":       storeNode(StoreSupabase, "", supabaseParams, "full", ""),
	N8nPrefix + "vectorStoreSupabaseInsert": storeNode(StoreSupabase, "insert", supabaseParams, "full", ""),
	N8nPrefix + "vectorStoreSupabaseLoad":   storeNode(StoreSupabase, "search", supabaseParams, "full", ""),
	N8nPrefix + "vectorStorePGVector": storeNode(StorePGVector, "", map[string]string{
		"tableName": "collection", "options.distanceStrategy": "distance",
		"options.columnNames.values.idColumnName":       "id_column",
		"options.columnNames.values.contentColumnName":  "content_column",
		"options.columnNames.values.metadataColumnName": "metadata_column",
		"options.columnNames.values.vectorColumnName":   "vector_column",
	}, "partial", "the collection-table mode (options.collection) is not supported; documents go straight into tableName"),
	N8nPrefix + "vectorStoreWeaviate": storeNode(StoreWeaviate, "", map[string]string{
		"weaviateCollection": "collection", "options.textKey": "text_key", "options.metadataKeys": "metadata_keys", "options.clearStore": "clear",
	}, "partial", "hybrid search, tenants and n8n's JSON search filter are not mapped; a search filter must be rewritten as GraphQL where text"),
	N8nPrefix + "vectorStoreMilvus": storeNode(StoreMilvus, "", map[string]string{
		"milvusCollection": "collection", "options.clearCollection": "clear",
	}, "full", ""),
	N8nPrefix + "vectorStoreMongoDBAtlas": storeNode(StoreMongoDBAtlas, "", map[string]string{
		"mongoCollection": "collection", "vectorIndexName": "vector_index", "embedding": "embedding_field",
		"metadata_field": "text_field", "options.preFilter": "filter", "options.postFilterPipeline": "post_filter_pipeline",
		"options.clearNamespace": "clear",
	}, "partial", "options.namespace (metadata namespacing) is not mapped"),
	N8nPrefix + "vectorStoreRedis": storeNode(StoreRedis, "", map[string]string{
		"redisIndex": "collection", "options.keyPrefix": "key_prefix", "options.metadataKey": "metadata_key",
		"options.contentKey": "content_key", "options.vectorKey": "vector_key", "options.metadataFilter": "filter",
		"options.overwriteDocuments": "clear",
	}, "partial", "document TTL (options.ttl) is not mapped"),
	N8nPrefix + "vectorStoreChromaDB": storeNode(StoreChroma, "", map[string]string{
		"chromaCollection": "collection", "options.clearCollection": "clear",
	}, "full", ""),
	N8nPrefix + "vectorStoreInMemory":       storeNode(StoreMemory, "", inMemoryParams, "full", "memory stores live for the process lifetime, as in n8n"),
	N8nPrefix + "vectorStoreInMemoryInsert": storeNode(StoreMemory, "insert", inMemoryParams, "full", ""),
	N8nPrefix + "vectorStoreInMemoryLoad":   storeNode(StoreMemory, "search", inMemoryParams, "full", ""),
	N8nPrefix + "vectorStoreZep":            {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: zepNote},
	N8nPrefix + "vectorStoreZepInsert":      {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: zepNote},
	N8nPrefix + "vectorStoreZepLoad":        {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: zepNote},
	N8nPrefix + "vectorStoreAzureAISearch":  {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: "no Azure AI Search provider"},
	N8nPrefix + "vectorStoreDatabricks":     {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: "no Databricks provider"},
	N8nPrefix + "vectorStoreOracleDBVector": {Role: N8nRoleNode, OurType: "vector.store", Coverage: "unsupported", Notes: "no Oracle DB provider"},

	// ── text splitters ──────────────────────────────────────────────────────
	N8nPrefix + "textSplitterCharacterTextSplitter": {Role: N8nRoleSplitter, OurType: "vector.split_text",
		Fixed:  map[string]interface{}{"splitter": SplitterCharacter},
		Params: map[string]string{"separator": "separator", "chunkSize": "chunk_size", "chunkOverlap": "chunk_overlap"}, Coverage: "full"},
	N8nPrefix + "textSplitterRecursiveCharacterTextSplitter": {Role: N8nRoleSplitter, OurType: "vector.split_text",
		Fixed:  map[string]interface{}{"splitter": SplitterRecursive},
		Params: map[string]string{"chunkSize": "chunk_size", "chunkOverlap": "chunk_overlap"}, Coverage: "partial",
		Notes: "language presets (options.splitCode) are not mapped; the default paragraph/line/word/character separators are used"},
	N8nPrefix + "textSplitterTokenSplitter": {Role: N8nRoleSplitter, OurType: "vector.split_text",
		Fixed:  map[string]interface{}{"splitter": SplitterToken},
		Params: map[string]string{"chunkSize": "chunk_size", "chunkOverlap": "chunk_overlap"}, Coverage: "partial",
		Notes: "token counts are approximate (no tokenizer bundled), so chunk boundaries differ from tiktoken's"},

	// ── document loaders ────────────────────────────────────────────────────
	N8nPrefix + "documentDefaultDataLoader": {Role: N8nRoleNode, OurType: "vector.load_document",
		Params: map[string]string{
			"dataType": "source", "jsonData": "text", "binaryDataKey": "binary_key", "loader": "loader",
			"options.splitPages": "split_pages", "options.separator": "csv_separator", "options.column": "csv_column",
		},
		Values:   map[string]map[string]interface{}{"loader": n8nLoaderValues},
		Coverage: "partial", Notes: "EPUB files, JSON pointers and the metadata option are not mapped"},
	N8nPrefix + "documentBinaryInputLoader": {Role: N8nRoleNode, OurType: "vector.load_document",
		Fixed: map[string]interface{}{"source": SourceBinary},
		Params: map[string]string{
			"binaryDataKey": "binary_key", "loader": "loader", "splitPages": "split_pages",
			"separator": "csv_separator", "column": "csv_column",
		},
		Values:   map[string]map[string]interface{}{"loader": n8nLoaderValues},
		Coverage: "partial", Notes: "EPUB files and JSON pointers are not mapped"},
	N8nPrefix + "documentJsonInputLoader": {Role: N8nRoleNode, OurType: "vector.load_document",
		Fixed: map[string]interface{}{"source": SourceJSON}, Coverage: "partial", Notes: "JSON pointers are not mapped; the whole item is loaded"},
	N8nPrefix + "documentGithubLoader": {Role: N8nRoleNode, Coverage: "unsupported",
		Notes: "fetch the files with service.github, then use vector.load_document"},

	// ── retrievers ──────────────────────────────────────────────────────────
	N8nPrefix + "retrieverVectorStore": {Role: N8nRoleRetriever, OurType: "vector.store",
		Fixed: map[string]interface{}{"mode": "search"}, Params: map[string]string{"topK": "top_k"}, Coverage: "full",
		Notes: "folded into its ai_vectorStore child as a search"},
	N8nPrefix + "retrieverContextualCompression": {Role: N8nRoleRetriever, Coverage: "unsupported", Notes: "needs an LLM in the retriever; use agent.ask on the search results"},
	N8nPrefix + "retrieverMultiQuery":            {Role: N8nRoleRetriever, Coverage: "unsupported", Notes: "needs an LLM in the retriever; use agent.ask to rewrite the query first"},
	N8nPrefix + "retrieverWorkflow":              {Role: N8nRoleRetriever, Coverage: "unsupported", Notes: "call the sub-workflow directly"},
}

// n8nLoaderValues maps n8n loader option values onto ours.
var n8nLoaderValues = map[string]interface{}{
	"auto": LoaderAuto, "csvLoader": LoaderCSV, "docxLoader": LoaderDocx, "jsonLoader": LoaderJSON,
	"pdfLoader": LoaderPDF, "textLoader": LoaderText, "epubLoader": LoaderAuto,
}

// N8nCredentialMapping maps an n8n credential type to a mono connection
// platform and its field names.
type N8nCredentialMapping struct {
	Platform string
	Fields   map[string]string // n8n field → our field
}

// N8nCredentialMappings is keyed by n8n credential type name.
var N8nCredentialMappings = map[string]N8nCredentialMapping{
	"openAiApi":           {"openai", map[string]string{"apiKey": "api_key", "url": "base_url"}},
	"azureOpenAiApi":      {"azure_openai", map[string]string{"apiKey": "api_key", "resourceName": "resource_name", "apiVersion": "api_version", "endpoint": "endpoint"}},
	"ollamaApi":           {"ollama", map[string]string{"baseUrl": "base_url", "apiKey": "api_key"}},
	"cohereApi":           {"cohere", map[string]string{"apiKey": "api_key", "url": "base_url"}},
	"mistralCloudApi":     {"mistral", map[string]string{"apiKey": "api_key"}},
	"googlePalmApi":       {"gemini_api", map[string]string{"apiKey": "api_key", "host": "base_url"}},
	"huggingFaceApi":      {"huggingface", map[string]string{"apiKey": "api_key"}},
	"aws":                 {"aws", map[string]string{"accessKeyId": "access_key_id", "secretAccessKey": "secret_access_key", "sessionToken": "session_token", "region": "region"}},
	"nvidiaApi":           {"openai", map[string]string{"apiKey": "api_key", "url": "base_url"}},
	"qdrantApi":           {"qdrant", map[string]string{"qdrantUrl": "url", "apiKey": "api_key"}},
	"pineconeApi":         {"pinecone", map[string]string{"apiKey": "api_key"}},
	"supabaseApi":         {"supabase", map[string]string{"host": "url", "serviceRole": "api_key"}},
	"weaviateApi":         {"weaviate", map[string]string{"weaviate_cloud_endpoint": "url", "weaviate_api_key": "api_key"}},
	"milvusApi":           {"milvus", map[string]string{"baseUrl": "url", "username": "username", "password": "password"}},
	"chromaSelfHostedApi": {"chroma", map[string]string{"baseUrl": "url", "apiKey": "api_key", "token": "api_key"}},
	"chromaCloudApi":      {"chroma", map[string]string{"baseUrl": "url", "apiKey": "api_key", "tenant": "tenant", "database": "database"}},
	// postgres / mongoDb / redis credentials split host, port, user … in
	// n8n; the importer builds connection_string for postgresql / mongodb
	// / redis the same way it does for the db.* nodes.
	"postgres": {"postgresql", nil},
	"mongoDb":  {"mongodb", nil},
	"redis":    {"redis", nil},
}
