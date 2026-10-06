-- Ledgerlite schema. Requires PostgreSQL 18 (built-in uuidv7()).
-- Money is stored as integers in paise. Never floats.

CREATE TABLE accounts (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL DEFAULT 'user' CHECK (kind IN ('user', 'external')),
    currency   CHAR(3)     NOT NULL DEFAULT 'INR',
    balance    BIGINT      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT balance_non_negative CHECK (kind = 'external' OR balance >= 0)
);

INSERT INTO accounts (name, kind) VALUES ('external', 'external');

CREATE TABLE transfers (
    id           UUID PRIMARY KEY DEFAULT uuidv7(),
    from_account UUID        NOT NULL REFERENCES accounts (id),
    to_account   UUID        NOT NULL REFERENCES accounts (id),
    amount       BIGINT      NOT NULL CHECK (amount > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_account <> to_account)
);

CREATE TABLE entries (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id UUID        NOT NULL REFERENCES transfers (id),
    account_id  UUID        NOT NULL REFERENCES accounts (id),
    amount      BIGINT      NOT NULL CHECK (amount <> 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX entries_account_idx  ON entries (account_id, id);
CREATE INDEX entries_transfer_idx ON entries (transfer_id);

-- Entries of one transfer must sum to zero, checked at COMMIT time.
CREATE FUNCTION check_transfer_balanced() RETURNS trigger AS $$
BEGIN
    IF (SELECT COALESCE(SUM(amount), 0) FROM entries
         WHERE transfer_id = NEW.transfer_id) <> 0 THEN
        RAISE EXCEPTION 'entries for transfer % do not sum to zero', NEW.transfer_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER entries_balanced
    AFTER INSERT ON entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_transfer_balanced();

-- History is immutable: no UPDATE or DELETE on ledger rows.
CREATE FUNCTION forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% on % is not allowed', TG_OP, TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER entries_immutable BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER transfers_immutable BEFORE UPDATE OR DELETE ON transfers
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE idempotency_keys (
    client_id       TEXT        NOT NULL DEFAULT 'default',
    key             TEXT        NOT NULL,
    request_hash    TEXT        NOT NULL,
    transfer_id     UUID        REFERENCES transfers (id),
    response_status INT,
    response_body   JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, key)
);

CREATE TABLE outbox (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    aggregate_id UUID        NOT NULL,
    type         TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
