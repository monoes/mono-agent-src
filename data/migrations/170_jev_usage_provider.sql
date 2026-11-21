-- 170_jev_usage_provider.sql
-- Which decision provider answered a Jev call, and what it cost (docs/mastermind/plans/2026-11-21-decision-providers.md,
-- WS0). Additive only: four nullable columns on jev_usage, nothing rebuilt. Counts only, as before: no request or answer
-- content.
--
--   provider          the provider's name ('typesafe', or a configured one); NULL on rows written before this migration,
--                     which were all TypeSafe's
--   output_tokens     output tokens the provider reported (TypeSafe's are free; other providers may bill them)
--   probability_kind  how the answer's probabilities were obtained: native | logprob | sampled | none
--   cost_usd          what the call cost when the provider said so or its rates are configured; NULL = unknown (a TypeSafe
--                     row's cost is then derived at read time from input_tokens, as before)
ALTER TABLE jev_usage ADD COLUMN provider TEXT;
ALTER TABLE jev_usage ADD COLUMN output_tokens INTEGER;
ALTER TABLE jev_usage ADD COLUMN probability_kind TEXT;
ALTER TABLE jev_usage ADD COLUMN cost_usd REAL;
