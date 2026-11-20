-- Intentionally irreversible: removing these guards would allow historical
-- rollback scripts to erase audit history.
DO $$ BEGIN
    RAISE EXCEPTION 'security migration 008 cannot be rolled back; retain schema and roll back application code, or follow the documented reviewed recovery procedure';
END $$;
