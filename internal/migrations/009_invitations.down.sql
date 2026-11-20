-- Invitation records and verified identity links must survive rollback.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 009 requires a reviewed forward recovery to preserve invitation state';
END $$;
