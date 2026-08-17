-- UpdateCustomer needs somewhere to record when a row last changed.
-- Existing rows get `now()` as their updated_at via the column default.
ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
