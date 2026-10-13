package connections

// apiKeyPlatform builds an API-key platform entry (used by the RAG block).
func apiKeyPlatform(id, name, category, icon string, fields ...CredentialField) PlatformDef {
	return PlatformDef{
		ID: id, Name: name, Category: category, ConnectVia: "API",
		Methods:   []AuthMethod{MethodAPIKey},
		Fields:    map[AuthMethod][]CredentialField{MethodAPIKey: fields},
		IconEmoji: icon,
	}
}
