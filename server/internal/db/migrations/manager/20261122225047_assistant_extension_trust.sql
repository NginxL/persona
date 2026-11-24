-- What an extension may read of the assistant's sensitive information (#13246, docs/assistant/health.md "Using
-- information by scene", scene 6). assistant_extension_trust says whether an extension is the user's own (trust
-- 'owner', such as their memory extension: it reads everything) or a third party's ('third_party'); an extension with
-- no row is a third party's. assistant_extension_grants are the sensitive categories the user let a third-party
-- extension read, one row each. Only the user changes either, in the app; trust values and categories are checked in
-- code.
CREATE TABLE IF NOT EXISTS assistant_extension_trust (
    extension_id text PRIMARY KEY,
    trust        text NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assistant_extension_grants (
    extension_id text NOT NULL,
    category     text NOT NULL,
    granted_at   timestamptz NOT NULL DEFAULT now(),
    granted_by   text NOT NULL,
    PRIMARY KEY (extension_id, category)
);
