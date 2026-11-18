-- Invitation records and verified identity links must survive rollback.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 007 requires a reviewed forward recovery to preserve invitation state';
END $$;
