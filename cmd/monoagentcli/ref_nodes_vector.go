package main

// Reference entries for the RAG node types in internal/nodes/vector.

func init() {
	nodeDocs = append(nodeDocs, vectorNodeDocs...)
}

var vectorNodeDocs = []nodeDoc{
	{
		Type:     "vector.embed",
		Category: "vector",
		Short:    "Turn item text into embedding vectors (OpenAI-compatible, Ollama, Cohere, Gemini, Mistral, HF, Bedrock, Azure)",
		Description: `Calls an embedding API once per batch and adds one vector per item under
output_key (default "embedding"). Replaces the deprecated ai.embed.

Providers and their connection (credential_id):
  openai_compatible  "openai" — OpenAI, OpenRouter-compatible and local servers
                     (vLLM, LM Studio, llama.cpp …) via base_url; key optional
  ollama             "ollama" — base_url defaults to http://localhost:11434, no key
  cohere             "cohere"            gemini     "gemini_api" (Gemini API key)
  mistral            "mistral"           huggingface "huggingface"
  bedrock            "aws" (access keys + region; Titan or Cohere models)
  azure_openai       "azure_openai" (resource_name or endpoint; model = deployment)

Text comes from input_field / input_fields (dotted paths; several are joined by a
blank line). Without them the first of pageContent, text, content is used, else the
whole item as JSON.`,
		Config: `{
  "provider":    "openai_compatible",
  "credential_id": "openai",
  "model":       "text-embedding-3-small",  // provider default when empty
  "base_url":    "",                        // override, e.g. http://localhost:1234/v1
  "input_field": "pageContent",
  "output_key":  "embedding",
  "batch_size":  100,
  "dimensions":  0,                         // >0: ask for shorter vectors where supported
  "purpose":     "document"                 // document | query (Cohere/Gemini input type)
}`,
		Inputs:  "items carrying text",
		Outputs: "the same items plus output_key: [float, …]",
		Notes:   "Defaults: openai text-embedding-3-small, ollama nomic-embed-text, cohere embed-english-v3.0, gemini text-embedding-004, mistral mistral-embed, huggingface sentence-transformers/all-MiniLM-L6-v2, bedrock amazon.titan-embed-text-v2:0.",
	},
	{
		Type:     "vector.split_text",
		Category: "vector",
		Short:    "Split long text into overlapping chunks (character, recursive or token)",
		Description: `Emits one item per chunk shaped like a document: {pageContent, metadata}. The
input item's "metadata" object is carried forward, metadata_fields adds item fields to
it, and chunk_index / chunk_count are added.

  character  split on one separator (default "\n\n") and pack pieces up to chunk_size
  recursive  try separators in order (default paragraph, line, word, character) and
             recurse into pieces that are still too long — no chunk exceeds chunk_size
  token      window of chunk_size approximate tokens. There is no tokenizer
             dependency: a token is a word piece of up to 4 letters, up to 3 digits or a
             punctuation run, which tracks OpenAI's cl100k counts to roughly ±20% on
             English prose (less closely on code and non-Latin scripts).

Sizes count Unicode characters, not bytes. chunk_overlap must be smaller than
chunk_size. In separator fields, \n, \t and \r are typed as two characters.`,
		Config: `{
  "splitter":        "recursive",          // character | recursive | token
  "chunk_size":      1000,
  "chunk_overlap":   0,
  "separator":       "\\n\\n",             // character only
  "separators":      ["\\n\\n", "\\n", " ", ""], // recursive only; "" = per character
  "input_field":     "pageContent",
  "metadata_fields": "url,title"
}`,
		Inputs:  "items carrying text (e.g. from vector.load_document)",
		Outputs: "one {pageContent, metadata} item per chunk",
	},
	{
		Type:     "vector.load_document",
		Category: "vector",
		Short:    "Load documents from item fields, binaries or files (txt, md, html, csv, json, docx, pdf)",
		Description: `Produces {pageContent, metadata} items for vector.split_text / vector.store.

  source json    text = the "text" template (evaluated per item), else input_field,
                 else pageContent/text/content, else the item as JSON
  source binary  item binary binary_key (default "data"); fileName / mimeType item
                 fields help auto-detection
  source file    file_path, or each item's file_path_field (default "file_path")

loader auto picks by extension, MIME type, then content. pdf yields one document per
page (split_pages=false joins them); csv one per row ("column: value" lines, or only
csv_column); html drops scripts/styles; docx reads the body text. All parsing is
built in (no external tools).

Set split=true to chunk in the same step with vector.split_text's fields (splitter,
chunk_size, chunk_overlap, separator(s)).`,
		Config: `{
  "source":        "binary",              // json | binary | file
  "loader":        "auto",                // auto | text | markdown | html | csv | json | docx | pdf
  "binary_key":    "data",
  "file_path":     "",
  "text":          "{{ $json.body }}",    // json source
  "split_pages":   true,                  // pdf
  "csv_separator": ",", "csv_column": "",
  "metadata_fields": "id,url",
  "split": true, "splitter": "recursive", "chunk_size": 1000, "chunk_overlap": 200
}`,
		Inputs:  "items (or none, with file_path / text set)",
		Outputs: "document items; metadata has source, loader and page / row / file_name",
		Notes:   "The PDF reader handles text drawn with simple fonts in plain or Flate streams. Scanned pages, encryption and CID fonts without a simple encoding (some CJK and subset PDFs) give empty or garbled text. Files over 64 MiB are refused.",
	},
	{
		Type:     "vector.store",
		Category: "vector",
		Short:    "Insert, search, update or delete documents in a vector database (Qdrant, Pinecone, pgvector, Supabase, …)",
		Description: `One node for every vector database. mode:
  insert  items → documents (text from content_field or pageContent/text/content,
          metadata from "metadata" + metadata_fields, id from id_field or a new UUID,
          vector from vector_field "embedding" or embedded inline); clear=true empties
          the collection first
  search  per input item: query (template, per item) / query_vector / the item's
          vector_field / query_field (default: query, chatInput, pageContent, text) →
          top_k hits as {id, pageContent, metadata, score, rank, query}
  update  like insert but every item must carry an id
  delete  ids, filter, the items' id_field values, or delete_all=true
n8n's load / retrieve / retrieve-as-tool modes run as search.

Vectors: put an "embedding" block in the config to embed inline (same fields as
vector.embed; its own credential_id), or run vector.embed first.

Providers (collection = collection / index / table / class; filter format differs):
  qdrant         url, api_key — filter: Qdrant filter object
  pinecone       api_key, index_host (else looked up), namespace — filter: Pinecone object
  pgvector       connection_string; table columns id/text/metadata/embedding
                 (id_column …); distance cosine|euclidean|inner_product — filter: JSON
                 object matched with metadata @> (containment)
  supabase       url, api_key (service role); table + match_documents RPC
                 (query_name) — filter: JSON object passed to the RPC
  weaviate       url, api_key; class; text_key; metadata_keys returned on search —
                 search filter: GraphQL where text; delete filter: where JSON object
  milvus         url, token or username/password — filter: Milvus expression string
  mongodb_atlas  connection_string, database, vector_index, embedding_field,
                 text_field — filter: $vectorSearch pre-filter object
  redis          connection_string (RediSearch); key_prefix — filter: RediSearch query
  chroma         url, api_key, tenant, database — filter: Chroma where object
  memory         memory_key; lives in this process only (lost on restart; a one-shot
                 "workflow run" starts empty) — filter: metadata key/value object
Qdrant, Milvus, Chroma, Redis and pgvector create the collection / index / table on
first insert. Score is the provider's similarity (higher = closer; distance-based
providers report 1 - distance).`,
		Config: `{
  "provider":   "qdrant",
  "credential_id": "qdrant",
  "mode":       "search",            // insert | search | update | delete
  "collection": "kb",
  "query":      "{{ $json.chatInput }}",
  "top_k":      4,
  "filter":     {"must":[{"key":"metadata.lang","match":{"value":"en"}}]},
  "embedding":  {"provider": "ollama", "model": "nomic-embed-text"}
}`,
		Inputs:  "documents to insert/update, queries to search, ids to delete",
		Outputs: "insert/update: the stored documents with ids; search: one item per hit; delete: {deleted, ids}",
		Notes: `Supabase setup, once, in the SQL editor (1536 = your model's dimensions):
  create extension if not exists vector;
  create table documents (id text primary key default gen_random_uuid()::text,
    content text, metadata jsonb, embedding vector(1536));
  create function match_documents(query_embedding vector(1536), match_count int,
    filter jsonb default '{}') returns table (id text, content text, metadata jsonb,
    similarity float) language sql stable as $$
    select id, content, metadata, 1 - (embedding <=> query_embedding) from documents
    where metadata @> filter order by embedding <=> query_embedding limit match_count $$;`,
	},
}
