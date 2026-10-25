-- Existing grants retain reference-only access; no row remains Strict.
ALTER TABLE profile_agent_secret_access ADD COLUMN level TEXT NOT NULL DEFAULT 'use' CHECK (level IN ('use', 'reveal'));
