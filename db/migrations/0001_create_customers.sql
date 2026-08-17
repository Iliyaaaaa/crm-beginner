-- Customers table: the persistent replacement for the in-memory map.
--
-- BIGSERIAL generates the id for us, which is why the Go code no longer
-- needs a nextID counter or a mutex to protect it.
CREATE TABLE IF NOT EXISTS customers (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL,
    email      TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The UNIQUE constraint on email is enforced by the database itself, not by
-- application code. Two concurrent requests with the same email cannot both
-- succeed: Postgres rejects the second with SQLSTATE 23505, which the store
-- layer translates into ErrDuplicateEmail.
