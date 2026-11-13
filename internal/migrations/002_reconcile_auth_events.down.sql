-- Version 1 came from two independent branches. We cannot determine which
-- tables existed before reconciliation, so an automatic rollback could delete
-- pre-existing user or event data. Refuse rollback rather than guess.
DO $$
BEGIN
    RAISE EXCEPTION 'Migration 2 is forward-only; restore a verified backup to roll back';
END;
$$;
