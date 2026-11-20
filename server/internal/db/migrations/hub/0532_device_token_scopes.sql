-- Scoped device tokens keep their grants beside the credential hash.
ALTER TABLE device_tokens ADD COLUMN scopes text[];
