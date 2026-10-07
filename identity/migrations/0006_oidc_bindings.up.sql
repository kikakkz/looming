CREATE TABLE oidc_bindings (
    issuer       text      NOT NULL,
    subject      text      NOT NULL,
    principal_id uuid      NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);

CREATE INDEX oidc_bindings_principal ON oidc_bindings(principal_id);
