-- AI data-sharing consent (owner level, migration 0074).

-- name: GetOwnerAiConsent :one
SELECT ai_consent_at, ai_consent_recipients FROM owner WHERE id = 1;

-- name: SetOwnerAiConsent :execrows
UPDATE owner SET ai_consent_at = @consented_at, ai_consent_recipients = @recipients WHERE id = 1;

-- name: ClearOwnerAiConsent :execrows
UPDATE owner SET ai_consent_at = NULL, ai_consent_recipients = '[]' WHERE id = 1;
